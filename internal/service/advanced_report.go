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

	acc, err := s.accounts.FindByID(ctx, accountID)
	if err != nil {
		return "", 0, apperr.NewNotFound("Account not found")
	}
	pdf, err := s.renderAdvancedReport(ctx, accountID, reportID, acc)
	if err != nil {
		return "", 0, err
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

// EmailAdvancedReport renders the advanced report and sends it to the address
// on the account.
//
// Returns [ErrReportEmailMissing] when there is none, exactly as the bureau
// report's email does, so the app can offer to link an address rather than
// reporting a failure the user cannot act on from that screen.
func (s *CreditAnalyticsService) EmailAdvancedReport(
	ctx context.Context, accountID, reportID int64,
) error {
	acc, err := s.accounts.FindByID(ctx, accountID)
	if err != nil {
		return apperr.NewNotFound("Account not found")
	}
	if acc.PrimaryEmail == nil || *acc.PrimaryEmail == "" {
		return ErrReportEmailMissing
	}
	if s.mailer == nil {
		return apperr.NewServiceUnavailable("Email delivery is not configured.")
	}

	pdf, err := s.renderAdvancedReport(ctx, accountID, reportID, acc)
	if err != nil {
		return err
	}
	filename := fmt.Sprintf("myScorr-advanced-report-%d.pdf", reportID)
	if err := s.mailer.SendAdvancedReport(*acc.PrimaryEmail, filename, pdf); err != nil {
		return apperr.NewBadGateway("Could not send the email. Please try again.")
	}
	return nil
}

// renderAdvancedReport builds the document for one report the caller owns and
// prints it, without storing anything.
//
// Shared by the download and the email so there is exactly one definition of
// what the report contains: two renderers would eventually put a different
// document in the mailbox from the one on screen.
func (s *CreditAnalyticsService) renderAdvancedReport(
	ctx context.Context, accountID, reportID int64, acc *models.Account,
) ([]byte, error) {
	if s.renderer == nil || !s.renderer.Available() {
		return nil, apperr.NewServiceUnavailable(
			"The advanced report is not available right now. Please try again later.")
	}
	row, err := s.findOwnedReport(ctx, accountID, reportID)
	if err != nil {
		return nil, err
	}
	insights, err := s.ReportInsightsFromRow(ctx, row)
	if err != nil {
		return nil, apperr.NewBadGateway("Could not read your report. Please try again.")
	}
	html, err := s.renderAdvancedHTML(acc, insights)
	if err != nil {
		slog.Error("advanced report: template failed",
			"account_id", accountID, "report_id", reportID, "error", err)
		return nil, apperr.NewBadGateway("Could not prepare your report. Please try again.")
	}
	pdf, err := s.renderer.PDF(ctx, html)
	if err != nil {
		slog.Error("advanced report: render failed",
			"account_id", accountID, "report_id", reportID, "error", err)
		return nil, apperr.NewBadGateway("Could not prepare your report. Please try again.")
	}
	return pdf, nil
}

// ---- the document's view model -------------------------------------------

type advancedReportView struct {
	HolderName string
	// PreparedOn is the long form for the cover ("5 September 2026"); ShortDate
	// is the running head's ("5 Sep 2026").
	PreparedOn string
	ShortDate  string
	ScoreText  string
	// ScoreLine is the score as it appears in the running head and the
	// projection, empty when the file carries none — the two places that read
	// wrong with an em dash in them.
	ScoreLine string
	Band      string

	FactorHeadline string
	Factors        []advancedFactor

	Highlights []advancedHighlight

	OpenAccountsHeadline   string
	OpenAccounts           []advancedAccount
	ClosedAccountsHeadline string
	ClosedAccounts         []advancedAccount
	HistorySince           string
	HistoryNote            string

	StreakCount  string
	PaymentYears []advancedPaymentYear

	ProjectionKicker string
	TargetRange      string
	// HoldNote replaces the arrow when the plan has no points to add: a file
	// with nothing left to fix gets a sentence, not a projection.
	HoldNote string
	Actions  []advancedAction
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
	// Colour is "teal" or "amber": the sample alternates them, and utilisation
	// is the amber one because it is the number a reader is meant to watch.
	Colour string
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
	// TagColour is "" (teal) or "amber" — amber for the tags that ask the reader
	// to go and do something rather than to keep doing it.
	TagColour string
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
		ShortDate:  in.CreatedAt.Format("2 Jan 2006"),
		ScoreText:  "—",
		Band:       "Not scored",
	}

	if in.CreditScore != nil {
		score := int(*in.CreditScore)
		v.ScoreText = fmt.Sprintf("%d", score)
		v.ScoreLine = v.ScoreText
		v.Band = strings.ToUpper(scoreBandLabel(score))
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
		v.ClosedAccountsHeadline = strings.ToUpper(
			countWord(len(closed), "closed account")) + ", ALL PAID IN FULL"
		// The day the file starts, from the earliest account OPENING date the
		// bureau reported. Deliberately not derived from payment history: that is
		// capped at ~36 months per tradeline, so a file open since February 2014
		// printed as April 2018 — the bug this replaced.
		if since := parseISODate(in.CreditHistorySince); !since.IsZero() {
			v.HistorySince = since.Format("January 2006")
			// The note is only true of the account that actually anchors the age.
			// A closed account that opened after some still-open one anchors
			// nothing, and saying it does is a claim the file contradicts.
			if company, closedYear, openedOn := oldestClosedAccount(in.LoanAccounts); company != "" &&
				openedOn == in.CreditHistorySince {
				if closedYear != "" {
					v.HistoryNote = fmt.Sprintf(
						"A %s account, closed in %s, still anchors your credit age today",
						company, closedYear)
				} else {
					v.HistoryNote = fmt.Sprintf(
						"A %s account, since closed, still anchors your credit age today", company)
				}
			}
		}
	}

	v.PaymentYears = advancedPaymentGrid(in.LoanAccounts)
	if streak := onTimeStreak(in.LoanAccounts); streak > 0 {
		v.StreakCount = fmt.Sprintf("%d", streak)
	}

	if sb := in.ScoreBuilder; sb != nil && len(sb.Strategies) > 0 {
		for _, st := range sb.Strategies {
			tag := strings.ToUpper(strings.TrimSpace(st.Tag))
			if tag == "" {
				tag = "DO"
			}
			v.Actions = append(v.Actions, advancedAction{
				Title:     st.Title,
				Detail:    st.Detail,
				Tag:       tag,
				TagColour: actionTagColour(tag),
			})
		}

		// What the page promises is what the list under it is worth — the sum of
		// the estimated points on these very actions, not the band the score sits
		// in. A file at 800 was printed "800 → 800–900", which is not a projection
		// at all: 900 is the top of the scale, and every action on a protect plan
		// is worth zero points by design because there is nothing left to fix.
		gainMin, gainMax := estimatedGain(sb.Strategies)
		switch {
		case gainMax > 0 && in.CreditScore != nil:
			score := int(*in.CreditScore)
			v.ProjectionKicker = projectionKicker(sb)
			v.TargetRange = scoreRange(score+gainMin, score+gainMax)
		case gainMax > 0:
			// Points to gain but no score to add them to: say what they are worth
			// rather than inventing a destination.
			v.ProjectionKicker = "What these actions are worth"
			v.TargetRange = scoreRange(gainMin, gainMax) + " points"
		default:
			// Nothing on this plan adds points. That is the ordinary state of a
			// strong file, and it is worth saying plainly instead of drawing an
			// arrow to the top of the scale.
			v.ProjectionKicker = "Where this file stands"
			v.HoldNote = "These actions protect your score rather than raise it — " +
				"at this level, holding it is the win."
			if in.CreditScore != nil {
				v.TargetRange = fmt.Sprintf("%d", *in.CreditScore)
			}
		}
	}

	return v
}

// estimatedGain adds up what the plan's own actions are estimated to be worth.
//
// Nil estimates are zero, not unknown-and-therefore-ignored: a strategy without
// numbers ("dispute any report errors") is one whose payoff nobody can promise,
// and rolling it into a range would be promising it.
func estimatedGain(strategies []BuilderStrategy) (lo, hi int) {
	for _, st := range strategies {
		if st.EstimatedPointsMin != nil {
			lo += *st.EstimatedPointsMin
		}
		if st.EstimatedPointsMax != nil {
			hi += *st.EstimatedPointsMax
		}
	}
	return lo, hi
}

// scoreRange renders "845–870", collapsing to one number when the ends meet and
// clamping to the top of the scale — a projection past 900 is arithmetic
// escaping the thing it describes.
func scoreRange(lo, hi int) string {
	const ceiling = 900
	if lo > ceiling {
		lo = ceiling
	}
	if hi > ceiling {
		hi = ceiling
	}
	if lo >= hi {
		return fmt.Sprintf("%d", hi)
	}
	return fmt.Sprintf("%d–%d", lo, hi)
}

// actionTagColour: teal for what the reader is already doing and should keep
// doing, amber for what asks them to go and act. The sample's own split.
func actionTagColour(tag string) string {
	switch tag {
	case "PROTECT", "KEEP", "MAINTAIN", "HOLD":
		return ""
	default:
		return "amber"
	}
}

// parseISODate reads a YYYY-MM-DD the server produced, or the zero time.
func parseISODate(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		return time.Time{}
	}
	return t
}

// oldestClosedAccount names the closed account that opened earliest — the one
// the history page's note is about — and the year it closed, when the bureau
// reported one.
func oldestClosedAccount(accounts []LoanAccount) (company, closedYear, openedOn string) {
	for _, a := range accounts {
		if a.Active || a.OpenedOn == "" {
			continue
		}
		if openedOn == "" || a.OpenedOn < openedOn {
			openedOn, company = a.OpenedOn, a.Company
			closedYear = ""
			if len(a.ClosedOn) >= 4 {
				closedYear = a.ClosedOn[:4]
			}
		}
	}
	return company, closedYear, openedOn
}

// accountYearRange renders "2014–22" from a tradeline's own dates: the year it
// opened, and the year it closed when the bureau reported one.
func accountYearRange(openedOn, closedOn string) string {
	if len(openedOn) < 4 {
		return ""
	}
	from := openedOn[:4]
	if len(closedOn) < 4 {
		return from
	}
	to := closedOn[:4]
	if to == from {
		return from
	}
	return from + "–" + to[2:]
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
			Colour:  "teal",
		})
	}
	if in.CardUtilizationPercent > 0 {
		out = append(out, advancedHighlight{
			Value:   fmt.Sprintf("%.0f%%", in.CardUtilizationPercent),
			Caption: utilisationCaption(in.CardUtilizationPercent),
			Colour:  "amber",
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
			Colour:  "teal",
		})
	}
	if in.ActiveAccountCount > 0 && len(out) < 3 {
		out = append(out, advancedHighlight{
			Value:   fmt.Sprintf("%d", in.ActiveAccountCount),
			Caption: "active accounts, all reporting",
			Colour:  "teal",
		})
	}
	if len(out) > 3 {
		out = out[:3]
	}
	return out
}

// utilisationCaption says what the number means rather than repeating it: well
// under the ideal ceiling is worth saying so, over it is worth saying plainly.
func utilisationCaption(pct float64) string {
	switch {
	case pct <= 15:
		return "card utilisation, half the ideal ceiling"
	case pct <= 30:
		return "card utilisation, under the ideal ceiling"
	default:
		return "card utilisation, above the ideal 30%"
	}
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
		// A chip, not a sentence: the company and the years it ran, off the
		// tradeline's own opening and closing dates. An account with neither is the
		// name alone rather than a span nobody told us.
		closed = append(closed, advancedAccount{
			Company: a.Company,
			Detail:  accountYearRange(a.OpenedOn, a.ClosedOn),
		})
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
