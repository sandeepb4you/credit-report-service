// Package service — the myScorr Advanced Report: the bureau's data, written up
// by us.
//
// Distinct from the Experian PDF the relay stores beside it. That one is the
// bureau's own document, encrypted with the holder's PAN and date of birth
// because it carries the raw file. This one is myScorr's reading of it — the
// score, the factor grades, the accounts, the payment record and the plan — and
// ships without a password by design: it is opened from inside an authenticated
// app, and asking someone to type their PAN to read a summary of themselves is
// the friction the document exists to remove.
//
// Everything in it is derived from a report already stored. Nothing here calls
// the bureau, and nothing here is typed: a figure that cannot be derived is a
// section that does not render, rather than a blank or a zero.
package service

import (
	"bytes"
	"context"
	_ "embed"
	"fmt"
	"html/template"
	"log/slog"
	"math"
	"sort"
	"strings"
	"time"

	"credit-report-service/internal/apperr"
	"credit-report-service/internal/models"
)

//go:embed templates/advanced_report.html
var advancedReportHTML string

// advancedReportTmpl is parsed once at startup: a template that fails to parse
// is a programming error, and finding it at boot beats finding it in a request.
var advancedReportTmpl = template.Must(
	template.New("advanced_report").Parse(advancedReportHTML))

// advancedReportKey is where a rendered report lives. Derivable from account and
// report id, like the bureau PDF beside it — which is why the bucket is private
// and reads are presigned.
func advancedReportKey(accountID, reportID int64) string {
	return fmt.Sprintf("advanced-reports/%d/%d.pdf", accountID, reportID)
}

// htmlRenderer is the browser seam. Narrowed here so the report can be built and
// asserted in tests with no Chromium on the machine.
type htmlRenderer interface {
	PDF(ctx context.Context, html string) ([]byte, error)
	Available() bool
}

// advancedReportStore is the object-store seam, write side included: this PDF is
// produced by us on demand rather than relayed, so it uploads as well as reads.
type advancedReportStore interface {
	UploadAs(ctx context.Context, key, filename, contentType string, body []byte) (string, error)
	PresignGet(ctx context.Context, keyOrURI string) (string, time.Duration, error)
	IsStub() bool
}

// SetAdvancedReportRenderer wires the browser. Optional, like every other
// upstream here: with none configured the endpoint reports the report
// unavailable rather than failing at boot.
func (s *CreditAnalyticsService) SetAdvancedReportRenderer(r htmlRenderer) {
	s.renderer = r
}

// SetAdvancedReportStore wires the object store this report is written to. A
// separate handle from the bureau PDF's read-only one because this document is
// produced here rather than relayed, so it uploads as well as reads.
func (s *CreditAnalyticsService) SetAdvancedReportStore(store advancedReportStore) {
	s.advancedStore = store
}

// AdvancedReportLink renders (or re-renders) the advanced report for a report
// the caller owns and returns a short-lived download URL.
//
// Rendered on demand rather than at pull time. A browser render is seconds of
// CPU and a few hundred MB of RAM, and most reports are never asked for in this
// form — doing it when somebody actually taps Download spends that on the ones
// that are. The object is kept afterwards, so a second tap is a presign.
func (s *CreditAnalyticsService) AdvancedReportLink(
	ctx context.Context, accountID, reportID int64,
) (string, time.Duration, error) {
	if s.advancedStore == nil || s.advancedStore.IsStub() {
		return "", 0, apperr.NewServiceUnavailable(
			"Report storage is not configured. Please try again later.")
	}
	if s.renderer == nil || !s.renderer.Available() {
		return "", 0, apperr.NewServiceUnavailable(
			"The advanced report is not available right now. Please try again later.")
	}

	row, err := s.findOwnedReport(ctx, accountID, reportID)
	if err != nil {
		return "", 0, err
	}
	insights, err := s.ReportInsightsFromRow(ctx, row)
	if err != nil {
		return "", 0, apperr.NewBadGateway("Could not read your report. Please try again.")
	}
	acc, err := s.accounts.FindByID(ctx, accountID)
	if err != nil {
		return "", 0, apperr.NewNotFound("Account not found")
	}

	html, err := s.renderAdvancedHTML(acc, insights)
	if err != nil {
		slog.Error("advanced report: template failed",
			"account_id", accountID, "report_id", reportID, "error", err)
		return "", 0, apperr.NewBadGateway("Could not prepare your report. Please try again.")
	}
	pdf, err := s.renderer.PDF(ctx, html)
	if err != nil {
		slog.Error("advanced report: render failed",
			"account_id", accountID, "report_id", reportID, "error", err)
		return "", 0, apperr.NewBadGateway("Could not prepare your report. Please try again.")
	}

	key := advancedReportKey(accountID, reportID)
	uri, err := s.advancedStore.UploadAs(ctx, key, "myScorr-advanced-report.pdf", "application/pdf", pdf)
	if err != nil {
		slog.Error("advanced report: upload failed",
			"account_id", accountID, "report_id", reportID, "error", err)
		return "", 0, apperr.NewBadGateway("Could not prepare the download. Please try again.")
	}
	url, ttl, err := s.advancedStore.PresignGet(ctx, uri)
	if err != nil {
		slog.Error("advanced report: presign failed",
			"account_id", accountID, "report_id", reportID, "error", err)
		return "", 0, apperr.NewBadGateway("Could not prepare the download. Please try again.")
	}
	slog.Info("advanced report generated",
		"account_id", accountID, "report_id", reportID, "bytes", len(pdf))
	return url, ttl, nil
}

// ---- the document's view model -------------------------------------------

type advancedReportView struct {
	HolderName    string
	PreparedOn    string
	ScoreText     string
	ScoreBarWidth int
	Band          string
	CoverHeadline string

	FactorHeadline string
	Factors        []advancedFactor

	Highlights []advancedHighlight

	OpenAccountsHeadline   string
	OpenAccounts           []advancedAccount
	ClosedAccountsHeadline string
	ClosedAccounts         []advancedAccount

	PaymentHeadline string
	PaymentYears    []advancedPaymentYear

	ProjectionKicker   string
	ProjectionHeadline string
	Actions            []advancedAction
}

type advancedFactor struct {
	Name      string
	Summary   string
	Verdict   string
	PillClass string
}

type advancedHighlight struct {
	Value   string
	Caption string
}

type advancedAccount struct {
	Company string
	Detail  string
	Amount  string
}

type advancedPaymentYear struct {
	Year  string
	Cells []string
}

type advancedAction struct {
	Title  string
	Detail string
	Tag    string
}

func (s *CreditAnalyticsService) renderAdvancedHTML(
	acc *models.Account, in *ReportInsights,
) (string, error) {
	var buf bytes.Buffer
	if err := advancedReportTmpl.Execute(&buf, buildAdvancedReportView(acc, in)); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// buildAdvancedReportView turns a stored report into the document.
//
// Every section is conditional on the data existing, because a bureau file
// legitimately arrives thin: no per-account detail, no factor grades, no score.
// A section that would have to invent a number is left out of the PDF entirely
// rather than printed empty — the document is a statement about somebody's
// finances, and a blank row in one reads as a fact.
func buildAdvancedReportView(acc *models.Account, in *ReportInsights) advancedReportView {
	v := advancedReportView{
		HolderName: accountDisplayName(acc),
		PreparedOn: in.CreatedAt.Format("2 January 2006"),
		ScoreText:  "—",
		Band:       "Not scored",
	}

	if in.CreditScore != nil {
		score := int(*in.CreditScore)
		v.ScoreText = fmt.Sprintf("%d", score)
		v.Band = strings.ToUpper(scoreBandLabel(score))
		// The bar is the same 300–900 range the app's gauge uses.
		frac := float64(score-300) / 600
		v.ScoreBarWidth = int(math.Round(math.Max(0, math.Min(1, frac)) * 210))
		v.CoverHeadline = coverHeadlineFor(score)
	} else {
		v.CoverHeadline = "Your credit file, read in full."
	}

	if in.ReportCard != nil && len(in.ReportCard.Factors) > 0 {
		for _, f := range in.ReportCard.Factors {
			verdict, class := factorVerdict(f.Grade)
			v.Factors = append(v.Factors, advancedFactor{
				Name:      f.Name,
				Summary:   f.Summary,
				Verdict:   verdict,
				PillClass: class,
			})
		}
		v.FactorHeadline = fmt.Sprintf("%s. %s",
			countWord(len(v.Factors), "factor"), factorVerdictHeadline(in.ReportCard))
	}

	v.Highlights = advancedHighlights(in)

	open, closed := splitAccounts(in.LoanAccounts)
	if len(open) > 0 {
		v.OpenAccounts = open
		v.OpenAccountsHeadline = fmt.Sprintf("%s, carrying you forward",
			countWord(len(open), "account"))
	}
	if len(closed) > 0 {
		v.ClosedAccounts = closed
		v.ClosedAccountsHeadline = fmt.Sprintf("%s, settled and behind you",
			countWord(len(closed), "account"))
	}

	v.PaymentYears = advancedPaymentGrid(in.LoanAccounts)
	if len(v.PaymentYears) > 0 {
		if streak := onTimeStreak(in.LoanAccounts); streak > 0 {
			v.PaymentHeadline = fmt.Sprintf("%d months, not one missed", streak)
		} else {
			v.PaymentHeadline = "Your payment record, month by month"
		}
	}

	if sb := in.ScoreBuilder; sb != nil && sb.TargetScoreMax > 0 && len(sb.Strategies) > 0 {
		v.ProjectionKicker = projectionKicker(sb)
		if in.CreditScore != nil {
			v.ProjectionHeadline = fmt.Sprintf("%d → %d–%d",
				*in.CreditScore, sb.TargetScoreMin, sb.TargetScoreMax)
		} else {
			v.ProjectionHeadline = fmt.Sprintf("Toward %d–%d", sb.TargetScoreMin, sb.TargetScoreMax)
		}
		for _, st := range sb.Strategies {
			v.Actions = append(v.Actions, advancedAction{
				Title:  st.Title,
				Detail: st.Detail,
				Tag:    strings.ToUpper(st.Tag),
			})
		}
	}
	return v
}

// accountDisplayName prefers the name on the account and falls back to the
// neutral "Your report" — a document addressed to nobody beats one addressed to
// an empty string.
func accountDisplayName(acc *models.Account) string {
	parts := []string{}
	if acc.FirstName != nil && strings.TrimSpace(*acc.FirstName) != "" {
		parts = append(parts, strings.TrimSpace(*acc.FirstName))
	}
	if acc.LastName != nil && strings.TrimSpace(*acc.LastName) != "" {
		parts = append(parts, strings.TrimSpace(*acc.LastName))
	}
	if len(parts) == 0 {
		return "Your report"
	}
	return strings.Join(parts, " ")
}

func scoreBandLabel(score int) string {
	switch {
	case score >= 800:
		return "Excellent"
	case score >= 750:
		return "Good"
	case score >= 650:
		return "Fair"
	default:
		return "Needs work"
	}
}

// coverHeadlineFor is the one piece of editorial in the document, and it is
// chosen rather than composed: a low score is met with the plan, never with a
// verdict (the design's rule — warm amber, never shaming red).
func coverHeadlineFor(score int) string {
	switch {
	case score >= 800:
		return "A file most lenders rarely see."
	case score >= 750:
		return "A strong file, with room at the top."
	case score >= 650:
		return "A solid base to build on."
	default:
		return "Every one of these is fixable."
	}
}

func factorVerdict(grade string) (string, string) {
	switch strings.ToUpper(strings.TrimSpace(grade)) {
	case "A+", "A":
		return "Clean", ""
	case "B", "C":
		return "Watch", "warn"
	default:
		return "Needs work", "bad"
	}
}

func factorVerdictHeadline(card *ReportCard) string {
	for _, f := range card.Factors {
		if v, _ := factorVerdict(f.Grade); v != "Clean" {
			return "Here is where they stand."
		}
	}
	return "All in your favour."
}

// countWord renders a small count as a word, the way the document reads it:
// "Four accounts" rather than "4 accounts".
func countWord(n int, noun string) string {
	words := []string{"No", "One", "Two", "Three", "Four", "Five", "Six", "Seven", "Eight", "Nine", "Ten"}
	plural := noun + "s"
	if n == 1 {
		plural = noun
	}
	if n >= 0 && n < len(words) {
		return words[n] + " " + plural
	}
	return fmt.Sprintf("%d %s", n, plural)
}

func advancedHighlights(in *ReportInsights) []advancedHighlight {
	out := []advancedHighlight{}
	if streak := onTimeStreak(in.LoanAccounts); streak > 0 {
		out = append(out, advancedHighlight{
			Value:   fmt.Sprintf("%d", streak),
			Caption: "consecutive months of on-time payments",
		})
	}
	if in.CardUtilizationPercent > 0 {
		out = append(out, advancedHighlight{
			Value:   fmt.Sprintf("%.0f%%", in.CardUtilizationPercent),
			Caption: "card utilisation, against an ideal ceiling of 30%",
		})
	}
	// The server's own figure, never a second derivation of it: the report card
	// grades credit age off the oldest OPEN account, while payment history only
	// goes back as far as the bureau reported months. Deriving it here produced a
	// document that said "6+ years" on page 3 and "15.5 years" on page 2.
	if in.CreditAgeYears != nil && *in.CreditAgeYears >= 1 {
		out = append(out, advancedHighlight{
			Value:   fmt.Sprintf("%d+", int(*in.CreditAgeYears)),
			Caption: "years of credit history behind you",
		})
	}
	if in.ActiveAccountCount > 0 && len(out) < 3 {
		out = append(out, advancedHighlight{
			Value:   fmt.Sprintf("%d", in.ActiveAccountCount),
			Caption: "active accounts, all reporting",
		})
	}
	if len(out) > 3 {
		out = out[:3]
	}
	return out
}

func splitAccounts(accounts []LoanAccount) (open, closed []advancedAccount) {
	for _, a := range accounts {
		last4 := lastFour(a.AccountNumber)
		if a.Active {
			detail := a.LoanType
			if last4 != "" {
				detail += " · ····" + last4
			}
			if a.InterestRatePercent > 0 {
				detail += fmt.Sprintf(" · %.1f%% p.a.", a.InterestRatePercent)
			}
			open = append(open, advancedAccount{
				Company: a.Company,
				Detail:  detail,
				Amount:  compactINR(a.CurrentBalance),
			})
			continue
		}
		detail := a.LoanType
		if last4 != "" {
			detail += " · ····" + last4
		}
		detail += " · closed, paid in full"
		closed = append(closed, advancedAccount{Company: a.Company, Detail: detail})
	}
	return open, closed
}

func lastFour(accountNumber string) string {
	digits := strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, accountNumber)
	if len(digits) < 4 {
		return ""
	}
	return digits[len(digits)-4:]
}

// compactINR prints an amount the way the document reads it: ₹1.14 Cr, ₹12.06 L.
func compactINR(amount float64) string {
	switch {
	case amount >= 1e7:
		return fmt.Sprintf("₹%.2f Cr", amount/1e7)
	case amount >= 1e5:
		return fmt.Sprintf("₹%.2f L", amount/1e5)
	case amount >= 1000:
		return fmt.Sprintf("₹%.0fK", amount/1000)
	default:
		return fmt.Sprintf("₹%.0f", amount)
	}
}

// advancedPaymentGrid merges every account's history into one row per year,
// worst status per month — the same rule the app's payment-history screen uses,
// so the PDF and the screen cannot disagree about the same month.
func advancedPaymentGrid(accounts []LoanAccount) []advancedPaymentYear {
	worst := map[string]string{} // "2026-08" -> class
	rank := map[string]int{"": 0, "paid": 1, "late": 2, "bad": 3}
	for _, a := range accounts {
		for _, m := range a.PaymentHistory {
			class := ""
			switch {
			case m.Status == "paid":
				class = "paid"
			case m.DaysLate >= 90:
				class = "bad"
			case m.Status == "delayed" || m.DaysLate > 0:
				class = "late"
			}
			if rank[class] > rank[worst[m.Month]] {
				worst[m.Month] = class
			}
		}
	}
	if len(worst) == 0 {
		return nil
	}
	years := map[string][]string{}
	for month, class := range worst {
		parts := strings.SplitN(month, "-", 2)
		if len(parts) != 2 {
			continue
		}
		if years[parts[0]] == nil {
			years[parts[0]] = make([]string, 12)
		}
		idx := monthIndex(parts[1])
		if idx >= 0 {
			years[parts[0]][idx] = class
		}
	}
	keys := make([]string, 0, len(years))
	for y := range years {
		keys = append(keys, y)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(keys)))
	out := make([]advancedPaymentYear, 0, len(keys))
	for _, y := range keys {
		out = append(out, advancedPaymentYear{Year: y, Cells: years[y]})
	}
	return out
}

func monthIndex(mm string) int {
	var n int
	if _, err := fmt.Sscanf(mm, "%d", &n); err != nil || n < 1 || n > 12 {
		return -1
	}
	return n - 1
}

// onTimeStreak counts back from the most recent reported month across every
// account, stopping at the first month anybody was late.
func onTimeStreak(accounts []LoanAccount) int {
	grid := advancedPaymentGrid(accounts)
	months := []string{}
	for _, y := range grid {
		for i := 11; i >= 0; i-- {
			months = append(months, y.Cells[i])
		}
	}
	streak := 0
	for _, class := range months {
		switch class {
		case "paid":
			streak++
		case "":
			continue // a month nobody reported breaks nothing
		default:
			return streak
		}
	}
	return streak
}

func projectionKicker(sb *ScoreBuilder) string {
	switch {
	case sb.TimelineMonthsMax > 0:
		return fmt.Sprintf("In %d months, disciplined, you could reach", sb.TimelineMonthsMax)
	default:
		return "Where this file can go"
	}
}
