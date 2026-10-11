package service

import (
	"bytes"
	"context"
	"encoding/csv"
	"fmt"
	"strconv"
	"strings"
	"time"

	"credit-report-service/internal/apperr"
	"credit-report-service/internal/models"
	"credit-report-service/internal/repository"
)

// AudienceService answers the console's retargeting lists (Admin ->
// Retargeting): eleven fixed lists of customers grouped by where they stalled,
// each exportable as a CSV shaped for one messaging channel.
//
// Two things it deliberately does not claim, because nothing records them:
//   - **marketing consent.** The S2 consent box gates sign-up client-side and
//     no endpoint stores the answer, so no row says a customer agreed to
//     WhatsApp, SMS or email marketing. The lists report who can be *reached*
//     — a phone, an address, a live push token — and the console says plainly
//     that reachable is not consented.
//   - **when a customer was last messaged**, or whether they viewed an offer.
//     Neither is logged server-side, so neither column exists.
type AudienceService struct {
	repo *repository.AudienceRepo
	now  func() time.Time
}

func NewAudienceService(repo *repository.AudienceRepo) *AudienceService {
	return &AudienceService{repo: repo, now: time.Now}
}

// utf8BOM opens every export so Excel reads ₹ and non-Latin names as UTF-8.
const utf8BOM = string(rune(0xFEFF))

// audienceMaxSelection bounds one export request. Generous against today's
// lists; a bound at all because the ids arrive from a client.
const audienceMaxSelection = 10000

func col(key, label, kind string) models.AudienceColumn {
	return models.AudienceColumn{Key: key, Label: label, Kind: kind}
}

// audienceCatalog is every list, in the order the console draws them. The
// definitions the descriptions promise live in repository.audienceQueries (and
// offersWaiting below for I); a description here must say what the query does.
var audienceCatalog = []models.AudienceList{
	{
		ID: "A", Group: models.AudienceGroupLost, Name: "Signed up, never paid",
		Description: "Verified a mobile number or email and has never started a payment. Most have no name on file yet — a name arrives with PAN verification, after payment — so address them as \"Hi there\". Anyone who did try to pay is in list B instead.",
		DateKey:     "signup", DateLabel: "Signup date", DefaultDays: 30,
		Columns: []models.AudienceColumn{col("signedWith", "Signed up with", "text"), col("daysSince", "Days since signup", "number")},
		Filters: []string{"platform", "reach"},
	},
	{
		ID: "B", Group: models.AudienceGroupLost, Name: "Payment failed or left checkout",
		Description: "Their most recent payment failed, expired, was cancelled, or has sat unpaid for over half an hour — and nothing has been paid since. Test payments from the internal app are left out.",
		DateKey:     "orderOn", DateLabel: "Payment attempt", DefaultDays: 30,
		Columns: []models.AudienceColumn{col("orderOn", "Attempted on", "date"), col("plan", "Plan", "text"), col("amount", "Amount", "money"), col("reason", "Reason", "pill")},
		Filters: []string{"platform", "plan", "reach"},
	},
	{
		ID: "C", Group: models.AudienceGroupLost, Name: "Paid, PAN not added",
		Description: "Paid, but has not submitted a PAN, so no score can be fetched. Urgent: the money has been taken and nothing has been delivered.",
		DateKey:     "paidOn", DateLabel: "Payment date", DefaultDays: 30,
		Columns: []models.AudienceColumn{col("paidOn", "Paid on", "date"), col("plan", "Plan", "text"), col("hours", "Hours waiting", "number")},
		Filters: []string{"platform", "plan", "reach"},
	},
	{
		ID: "D", Group: models.AudienceGroupLost, Name: "PAN in review or rejected",
		Description: "Submitted a PAN that is waiting for your manual check, or was rejected and needs resubmitting. This is also your KYC review queue.",
		DateKey:     "submitted", DateLabel: "PAN submitted", DefaultDays: 30,
		Columns: []models.AudienceColumn{col("submitted", "Submitted on", "date"), col("panStatus", "PAN status", "pill"), col("rejReason", "Reject reason", "text")},
		Filters: []string{"platform", "panStatus", "reach"},
	},
	{
		ID: "E", Group: models.AudienceGroupLost, Name: "Score ready, report not opened",
		Description: "A report arrived without them watching — a scheduled plan refresh, or a report run after a manual PAN approval — and the app has not been opened since. App opens are only recorded on a cold start on Android (and roughly weekly otherwise), so a recent visit can be missed.",
		DateKey:     "readyOn", DateLabel: "Score ready date", DefaultDays: 30,
		Columns: []models.AudienceColumn{col("readyOn", "Score ready on", "date"), col("plan", "Plan", "text"), col("band", "Score band", "text")},
		Filters: []string{"platform", "plan", "band", "reach"},
	},
	{
		ID: "F", Group: models.AudienceGroupBack, Name: "Score is getting old",
		Description: "No plan running, and the latest score is 30 to 120 days old. Filter the score band and the date for, say, \"around 650, two to three months ago\".",
		DateKey:     "scoreOn", DateLabel: "Last score date",
		// All time: the list is scores 30-120 days old, so "last 30 days" is empty by definition.
		DefaultDays: 0,
		Columns:     []models.AudienceColumn{col("scoreOn", "Last score on", "date"), col("score", "Score", "number"), col("band", "Score band", "text"), col("age", "Score age (days)", "number")},
		Filters:     []string{"platform", "band", "reach"},
	},
	{
		ID: "G", Group: models.AudienceGroupBack, Name: "Plan check due or expiring",
		Description: "On a prepaid plan, with a refresh due in the next 7 days or already overdue, or a plan year ending within 14 days.",
		DateKey:     "nextCheck", DateLabel: "Next check date", DateIsFuture: true, DefaultDays: 30,
		Columns: []models.AudienceColumn{col("plan", "Plan", "text"), col("nextCheck", "Next check", "date"), col("missed", "Overdue checks", "number"), col("planEnds", "Plan ends", "date")},
		Filters: []string{"platform", "plan", "reach"},
	},
	{
		ID: "H", Group: models.AudienceGroupBack, Name: "Gone quiet",
		Description: "Has a score and has not opened the app for 30 days or more.",
		DateKey:     "lastSeen", DateLabel: "Last seen",
		// All time: the list is people unseen for 30+ days, so "last 30 days" is empty by definition.
		DefaultDays: 0,
		Columns:     []models.AudienceColumn{col("lastSeen", "Last seen", "date"), col("inactive", "Days inactive", "number"), col("plan", "Plan", "text"), col("band", "Score band", "text")},
		Filters:     []string{"platform", "plan", "band", "reach"},
	},
	{
		ID: "I", Group: models.AudienceGroupBack, Name: "Offers waiting",
		Description: "Their latest report is an Excellent or Good match for a business loan — the same rule the Business loans screen applies. It is the only offer live today. Whether they viewed or applied for it is not recorded.",
		DateKey:     "readyOn", DateLabel: "Score date", DefaultDays: 30,
		Columns: []models.AudienceColumn{col("product", "Product", "text"), col("provider", "Provider", "text"), col("match", "Match", "pill"), col("readyOn", "Score date", "date")},
		Filters: []string{"platform", "product", "reach"},
	},
	{
		ID: "J", Group: models.AudienceGroupGrow, Name: "No email added",
		Description: "Signed up with a mobile number and never added an email address. Ask in the app or by push.",
		DateKey:     "signup", DateLabel: "Signup date", DefaultDays: 30,
		Columns: []models.AudienceColumn{col("paid", "Paid", "text"), col("daysSince", "Days since signup", "number")},
		Filters: []string{"platform", "paid", "reach"},
	},
	{
		ID: "K", Group: models.AudienceGroupGrow, Name: "Referral sharers",
		Description: "A score of 700 or more, their own referral code, and fewer than three friends brought in so far. Send them their code to share.",
		DateKey:     "signup", DateLabel: "Signup date", DefaultDays: 30,
		Columns: []models.AudienceColumn{col("code", "Referral code", "mono"), col("friends", "Friends joined", "number"), col("earned", "Earned", "money"), col("band", "Score band", "text")},
		Filters: []string{"platform", "band", "reach"},
	},
}

func catalogEntry(id string) (models.AudienceList, bool) {
	for _, l := range audienceCatalog {
		if l.ID == id {
			return l, true
		}
	}
	return models.AudienceList{}, false
}

// Lists returns every list with its current size.
func (s *AudienceService) Lists(ctx context.Context) ([]models.AudienceList, error) {
	out := make([]models.AudienceList, 0, len(audienceCatalog))
	for _, l := range audienceCatalog {
		rows, err := s.rows(ctx, l.ID, repository.AudienceRowLimit)
		if err != nil {
			return nil, err
		}
		l.Count = min(len(rows), repository.AudienceRowLimit)
		out = append(out, l)
	}
	return out, nil
}

// Rows returns one list and its accounts.
func (s *AudienceService) Rows(ctx context.Context, id string) (*models.AudienceListRows, error) {
	list, ok := catalogEntry(id)
	if !ok {
		return nil, apperr.NewNotFound("No such list")
	}
	rows, err := s.rows(ctx, id, repository.AudienceRowLimit)
	if err != nil {
		return nil, err
	}
	truncated := len(rows) > repository.AudienceRowLimit
	if truncated {
		rows = rows[:repository.AudienceRowLimit]
	}
	list.Count = len(rows)
	return &models.AudienceListRows{List: list, Rows: rows, Truncated: truncated}, nil
}

// rows reads a list and adds the derived score band wherever a score is
// present — one definition of the band boundaries, here, rather than one per
// query.
func (s *AudienceService) rows(ctx context.Context, id string, limit int) ([]models.AudienceRow, error) {
	var rows []models.AudienceRow
	var err error
	if id == "I" {
		rows, err = s.offersWaiting(ctx, limit)
	} else {
		rows, err = s.repo.AudienceRows(ctx, id, limit)
	}
	if err != nil {
		return nil, err
	}
	for i := range rows {
		if rows[i].Fields == nil {
			rows[i].Fields = map[string]string{}
		}
		if score, err := strconv.Atoi(rows[i].Fields["score"]); err == nil && score > 0 {
			rows[i].Fields["band"] = scoreBand(score)
		}
	}
	return rows, nil
}

// offersWaiting is list I. The business-loan rule is Go code
// (businessLoanMatch) rather than SQL, so it is applied here to each scored
// report rather than re-expressed in a query that could drift from it.
func (s *AudienceService) offersWaiting(ctx context.Context, limit int) ([]models.AudienceRow, error) {
	cands, err := s.repo.OfferCandidates(ctx)
	if err != nil {
		return nil, err
	}
	out := []models.AudienceRow{}
	for _, c := range cands {
		accounts, err := matchAccounts(c.ResponseBody)
		if err != nil {
			continue // an unreadable payload cannot be called a match
		}
		var match string
		switch businessLoanMatch(c.Score, accounts) {
		case BusinessLoanExcellent:
			match = "Excellent"
		case BusinessLoanGood:
			match = "Good"
		default:
			continue
		}
		row := c.Row
		row.Fields = map[string]string{
			"product":  "Business loan",
			"provider": businessLoanLenderName,
			"match":    match,
			"readyOn":  c.ReportAt.UTC().Format("2006-01-02"),
			"score":    strconv.FormatInt(c.Score, 10),
		}
		out = append(out, row)
		if len(out) > limit {
			break
		}
	}
	return out, nil
}

// scoreBand is the console's five buckets. Not the app's four credit bands:
// these are campaign slices, and "around 650" needs a boundary at 650.
func scoreBand(score int) string {
	switch {
	case score < 650:
		return "Below 650"
	case score < 700:
		return "650 to 699"
	case score < 750:
		return "700 to 749"
	case score < 800:
		return "750 to 799"
	default:
		return "800+"
	}
}

// Export builds the CSV for the selected accounts of one list, for one
// channel, and records it. The selection is intersected with the list as it
// stands now, so this route can only ever export accounts that are in the
// list — it cannot be pointed at arbitrary ids.
//
// The file is generated (and audited) before anyone presses Download: the
// console previews it, and a preview on screen is the data out of the system
// just as much as a file on disk.
func (s *AudienceService) Export(ctx context.Context, adminID int64, id, channel string, accountIDs []int64) (*models.AudienceExport, error) {
	list, ok := catalogEntry(id)
	if !ok {
		return nil, apperr.NewNotFound("No such list")
	}
	switch channel {
	case models.AudienceChannelWhatsApp, models.AudienceChannelSMS, models.AudienceChannelEmail,
		models.AudienceChannelPush, models.AudienceChannelAll:
	default:
		return nil, apperr.NewValidation("channel must be whatsapp, sms, email, push or all")
	}
	if len(accountIDs) == 0 {
		return nil, apperr.NewValidation("Select at least one account")
	}
	if len(accountIDs) > audienceMaxSelection {
		return nil, apperr.NewValidation(fmt.Sprintf("At most %d accounts per file", audienceMaxSelection))
	}

	rows, err := s.rows(ctx, id, repository.AudienceRowLimit)
	if err != nil {
		return nil, err
	}
	wanted := make(map[int64]bool, len(accountIDs))
	for _, aid := range accountIDs {
		wanted[aid] = true
	}
	var selected, included []models.AudienceRow
	for _, r := range rows {
		if !wanted[r.AccountID] {
			continue
		}
		selected = append(selected, r)
		if reachable(channel, r) {
			included = append(included, r)
		}
	}

	body, err := audienceCSV(list, channel, included)
	if err != nil {
		return nil, err
	}
	if err := s.repo.RecordAudienceExport(ctx, adminID, id, channel, len(selected), len(included)); err != nil {
		return nil, err
	}
	out := &models.AudienceExport{
		Filename: fmt.Sprintf("myscorr_list%s_%s_%s.csv", id, channel, s.now().Format("2006-01-02")),
		CSV:      body,
		Selected: len(selected),
		Included: len(included),
		LeftOut:  len(selected) - len(included),
	}
	if out.LeftOut > 0 {
		out.LeftOutReason = leftOutReason(channel)
	}
	return out, nil
}

// reachable is whether a channel can reach an account at all. Reachability,
// not consent — see the type comment.
func reachable(channel string, r models.AudienceRow) bool {
	switch channel {
	case models.AudienceChannelWhatsApp, models.AudienceChannelSMS:
		return r.Phone != ""
	case models.AudienceChannelEmail:
		return r.Email != ""
	case models.AudienceChannelPush:
		return r.CanPush
	default:
		return true
	}
}

func leftOutReason(channel string) string {
	switch channel {
	case models.AudienceChannelEmail:
		return "no email address"
	case models.AudienceChannelPush:
		return "no device with notifications on"
	default:
		return "no mobile number"
	}
}

// audienceCSV writes one channel's file. Messaging channels carry only what
// the sending tool needs plus the list's first two columns as personalisation
// fields (formatted for a sentence: "05 Oct 2026", "₹299"); "all" carries every
// column, raw, for analysis.
func audienceCSV(list models.AudienceList, channel string, rows []models.AudienceRow) (string, error) {
	var buf bytes.Buffer
	// A byte-order mark, so Excel opens ₹ and non-Latin names as UTF-8.
	buf.WriteString(utf8BOM)
	w := csv.NewWriter(&buf)

	personal := list.Columns
	if len(personal) > 2 {
		personal = personal[:2]
	}
	firstName := func(r models.AudienceRow) string {
		if r.FirstName != "" {
			return r.FirstName
		}
		return "there"
	}

	var header []string
	var line func(models.AudienceRow) []string
	switch channel {
	case models.AudienceChannelWhatsApp, models.AudienceChannelSMS:
		header = append([]string{"phone", "first_name"}, columnHeaders(personal)...)
		line = func(r models.AudienceRow) []string {
			return append([]string{r.Phone, firstName(r)}, humanValues(personal, r)...)
		}
	case models.AudienceChannelEmail:
		header = append([]string{"email", "first_name"}, columnHeaders(personal)...)
		line = func(r models.AudienceRow) []string {
			return append([]string{r.Email, firstName(r)}, humanValues(personal, r)...)
		}
	case models.AudienceChannelPush:
		header = []string{"user_id", "platform"}
		line = func(r models.AudienceRow) []string {
			return []string{strconv.FormatInt(r.AccountID, 10), r.Platform}
		}
	default:
		header = append([]string{"user_id", "first_name", "last_name", "phone", "email", "platform", "signup_date"},
			columnHeaders(list.Columns)...)
		header = append(header, "push_ok")
		line = func(r models.AudienceRow) []string {
			v := []string{strconv.FormatInt(r.AccountID, 10), r.FirstName, r.LastName, r.Phone, r.Email,
				r.Platform, r.SignedUpAt.UTC().Format("2006-01-02")}
			for _, c := range list.Columns {
				v = append(v, rawValue(c, r.Fields[c.Key]))
			}
			return append(v, yesNo(r.CanPush))
		}
	}

	if err := w.Write(header); err != nil {
		return "", err
	}
	for _, r := range rows {
		if err := w.Write(line(r)); err != nil {
			return "", err
		}
	}
	w.Flush()
	return buf.String(), w.Error()
}

// columnHeaders turns column labels into CSV-safe snake_case headers.
func columnHeaders(cols []models.AudienceColumn) []string {
	out := make([]string, len(cols))
	for i, c := range cols {
		var b strings.Builder
		lastUnderscore := true
		for _, r := range strings.ToLower(c.Label) {
			if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
				b.WriteRune(r)
				lastUnderscore = false
			} else if !lastUnderscore {
				b.WriteByte('_')
				lastUnderscore = true
			}
		}
		out[i] = strings.TrimSuffix(b.String(), "_")
	}
	return out
}

func humanValues(cols []models.AudienceColumn, r models.AudienceRow) []string {
	out := make([]string, len(cols))
	for i, c := range cols {
		v := r.Fields[c.Key]
		switch c.Kind {
		case "date":
			if t, err := time.Parse("2006-01-02", v); err == nil {
				v = t.Format("02 Jan 2006")
			}
		case "money":
			if p, err := strconv.ParseInt(v, 10, 64); err == nil {
				v = "₹" + strconv.FormatInt(p/100, 10)
			}
		}
		out[i] = v
	}
	return out
}

// rawValue is a column for the analysis file: dates stay ISO, money becomes
// rupees with two decimals — what a spreadsheet sums.
func rawValue(c models.AudienceColumn, v string) string {
	if c.Kind == "money" {
		if p, err := strconv.ParseInt(v, 10, 64); err == nil {
			return fmt.Sprintf("%d.%02d", p/100, p%100)
		}
	}
	return v
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}
