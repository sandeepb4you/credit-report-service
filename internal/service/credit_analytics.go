package service

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"math/big"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"credit-report-service/internal/apperr"
	"credit-report-service/internal/config"
	"credit-report-service/internal/digitap"
	"credit-report-service/internal/models"
	"credit-report-service/internal/repository"
)

// Hardcoded values for the Digitap /credit_analytics/request payload, per the
// product decision to build the request server-side rather than accept it from
// the client.
const (
	consentMessage = "I hereby authorize Experian to pull my credit report for accessing from financial profile"
	consentYes     = "yes"
	deviceTypeWeb  = "web"
	nameLookupOff  = 0 // name_lookup: 0 = use the supplied first/last name
	// reportTypeFlag selects the response format. Spec V2.7 s1.4.2.1 (the PDF in
	// docs/digitap/) lists: 0 or omitted = JSON only, 1 = XML pre-signed URL ONLY,
	// 2 = JSON + XML, 3 = JSON + PDF, 4 = JSON + PDF + XML. The URLs live about an
	// hour, which is why the PDF is relayed (download, encrypt, S3) rather than
	// handed to the app.
	//
	// 4 because we need result_json AND result_pdf, and the two narrower values
	// each failed in prod:
	//
	//   - 3 (JSON + PDF) is what the spec says to use, but every pull came back
	//     with "result_pdf": null, so the relay never had a link to fetch. The
	//     spec notes the PDF format is customized per client via the RM, so a
	//     provisioning gap on their side is the likely cause -- and if 4 also
	//     returns null, that is the finding to take back to them.
	//   - 1 was the RM's advice for getting a PDF, and it is XML-only: no
	//     result_json at all, so the score parser had nothing to read.
	//
	// 4 asks for the XML too, which we ignore -- there is no narrower value that
	// pairs JSON with PDF apart from the 3 that does not deliver one.
	reportTypeFlag = 4
	otpDigits      = 6

	// timestampLayout matches the Digitap spec's DDMMYYYY-HH:MM:SS format.
	timestampLayout = "02012006-15:04:05"

	// clientRefPrefix tags generated correlation ids so they're recognizable in
	// the upstream logs. The full form is "CA-<unixmilli>-<6 hex chars>".
	clientRefPrefix = "CA"
)

// CreditAnalyticsService proxies a credit-analysis request to the Digitap
// /credit_analytics/request API and persists both the request and the upstream
// response to the credit_analytics_requests table.
type CreditAnalyticsService struct {
	client   *digitap.Client
	repo     *repository.CreditAnalyticsRepo
	accounts *repository.AccountRepo
	// orders is the paywall. A bureau pull is a product the user buys, and this
	// is what proves they did.
	//
	// Required, not one of the optional Set* dependencies below: those degrade to
	// a less rich report when unwired, which is a visible loss. An unwired
	// paywall degrades to free bureau calls billed to us, and nothing on screen
	// would say so. A constructor argument cannot be forgotten.
	orders *repository.OrderRepo
	// reuseWindow answers a pull with a recent successful report instead of
	// calling Digitap. Zero disables it. See config.CreditAnalyticsConfig.
	reuseWindow time.Duration
	// loanSwitch is optional: when set, insights responses are enriched with
	// balance-transfer opportunities. Injected after construction (the two
	// services share the analytics repo) via SetLoanSwitch.
	loanSwitch *LoanSwitchService
	// scoreBuilder is optional: when set, the score-builder toolkit (S28)
	// surfaces admin-curated bank offerings instead of the generic FD-card text.
	// Injected after construction via SetScoreBuilder.
	scoreBuilder *ScoreBuilderService
	// pdfUploader is optional: when set, a successful report_type 3 pull
	// enqueues the Digitap result_pdf for async download, encryption and upload
	// to S3. When unset, result_pdf (if any) is ignored. Via SetPDFUploader.
	pdfUploader *ReportUploader
	// pdfStore is the read side of that storage, for the download and email
	// endpoints. Optional: unset or stubbed, both report the PDF unavailable
	// rather than failing at boot. Via SetReportPDFStore.
	pdfStore reportPDFStore
	// mailer sends the report as an attachment. Optional for the same reason.
	// Via SetReportMailer.
	mailer ReportMailer
}

// ReportMailer is the one mail capability this service needs. Narrow on purpose:
// the analytics service has no business sending OTPs, and a wide interface here
// would let it.
type ReportMailer interface {
	SendCreditReport(toEmail, filename string, pdf []byte) error
}

// SetReportMailer wires report delivery by email.
func (s *CreditAnalyticsService) SetReportMailer(m ReportMailer) { s.mailer = m }

func NewCreditAnalyticsService(
	client *digitap.Client,
	repo *repository.CreditAnalyticsRepo,
	accounts *repository.AccountRepo,
	orders *repository.OrderRepo,
	cfg config.CreditAnalyticsConfig,
) *CreditAnalyticsService {
	return &CreditAnalyticsService{
		client: client, repo: repo, accounts: accounts, orders: orders,
		reuseWindow: cfg.ReuseWindow,
	}
}

// SetLoanSwitch wires the loan-switch service used to enrich insights with
// interest-reduction opportunities. Optional; when unset, insights simply carry
// no interestSavings and no interest recommendations.
func (s *CreditAnalyticsService) SetLoanSwitch(ls *LoanSwitchService) {
	s.loanSwitch = ls
}

// SetScoreBuilder wires the score-builder service used to enrich the toolkit
// with admin-curated bank offerings. Optional; when unset, the toolkit falls
// back to the generic FD-card advice for the rebuild journey.
func (s *CreditAnalyticsService) SetScoreBuilder(sb *ScoreBuilderService) {
	s.scoreBuilder = sb
}

// SetPDFUploader wires the async PDF relay. Optional; when set, a successful
// pull enqueues the Digitap result_pdf for download, encryption and upload to
// S3. When unset, result_pdf is ignored (the JSON report is still stored).
func (s *CreditAnalyticsService) SetPDFUploader(u *ReportUploader) {
	s.pdfUploader = u
}

// CreditAnalyticsInput is the validated payload for a credit-analytics request.
// The Digitap payload is built server-side; these two are all the caller
// contributes.
type CreditAnalyticsInput struct {
	DeviceIP string `json:"device_ip"`

	// IdempotencyKey makes the pull safe to repeat. Optional — omitting it keeps
	// the old behaviour where every call is a new billed request — but the app
	// always sends one, because this endpoint costs the user money twice over: a
	// Digitap call we are billed for, and one of their paid orders spent.
	//
	// The key must identify the ATTEMPT, not the request: a client that mints a
	// fresh one on every retry has bought nothing, since the whole point is that
	// a re-entered screen sends the same key the first entry did.
	IdempotencyKey string `json:"idempotency_key"`
}

// ReportSummary is the trimmed list-item shape for the reports endpoint: the
// report's unique identifier (id), generation date (created_at), and the bureau
// score (nil when the pull failed or returned no record).
type ReportSummary struct {
	ID          int64     `json:"id"`
	CreatedAt   time.Time `json:"createdAt"`
	CreditScore *int64    `json:"creditScore"`
}

// ReportPage is the paginated reports-list response.
type ReportPage struct {
	Items []ReportSummary `json:"items"`
	Page  int             `json:"page"`
	Size  int             `json:"size"`
	Total int64           `json:"total"`
}

// reportFreshWindow is how long a bureau pull is treated as current. Bureaus
// refresh on roughly monthly lender-reporting cycles, so a report older than
// this may already be describing a file that has moved on.
const reportFreshWindow = 30 * 24 * time.Hour

// ReportInsights is the derived analytics from the latest successful credit
// report: the bureau credit score, on-time payment percentage, card
// utilization percentage, and enquiry count for the past 180 days.
type ReportInsights struct {
	ReportID    int64  `json:"reportId"`
	CreditScore *int64 `json:"creditScore"`
	// OnTimePaymentPercent is nil when no month of payment history was reported
	// on any tradeline — which is different from 0%, and used to be indistinguishable
	// from it. A thin or brand-new file showed "0% on time", read as "never paid
	// anything on time", and graded the payment factor F/Critical off no data at all.
	// The clients already model this as nullable and render "—".
	OnTimePaymentPercent   *float64 `json:"onTimePaymentPercent"`
	CardUtilizationPercent float64  `json:"cardUtilizationPercent"`
	EnquiryCount180Days    int64    `json:"enquiryCount180Days"`
	// Outdated is true once the report is older than reportFreshWindow. Clients
	// must drive "time to refresh" off this flag rather than re-deriving it from
	// CreatedAt: the window is a product decision, and two implementations of it
	// would eventually disagree about whether the same report is stale.
	Outdated bool `json:"outdated"`
	// CreatedAt is when the bureau pull ran — what a client shows as "checked on".
	CreatedAt              time.Time `json:"createdAt"`
	TotalAccountCount      int64     `json:"totalAccountCount"`
	ActiveAccountCount     int64     `json:"activeAccountCount"`
	TotalOutstandingAmount float64   `json:"totalOutstandingAmount"`
	MonthlyEMI             float64   `json:"monthlyEmi"`
	InterestPaidPerYear    float64   `json:"interestPaidPerYear"`
	// DerogatoryAccounts counts written-off / settled / defaulted tradelines —
	// the serious negatives the "good news" diagnosis checks for.
	DerogatoryAccounts int           `json:"derogatoryAccounts"`
	LoanAccounts       []LoanAccount `json:"loanAccounts"`
	ReportCard         *ReportCard   `json:"reportCard"`

	// InterestSavings holds the balance-transfer opportunities computed from the
	// report's active loans (nil when the loan-switch feature isn't wired in).
	InterestSavings *SwitchOpportunities `json:"interestSavings,omitempty"`
	// Recommendations is a single prioritized list of actions the user can take,
	// spanning both levers: improving the score (from the report card) and
	// reducing interest (from recommended switches).
	Recommendations []Recommendation `json:"recommendations"`
	// ScoreBuilder is the credit-score diagnosis + rebuild plan (Journey 05·C):
	// journey classification, a realistic target, the positives on file, what's
	// dragging the score, and a toolkit of strategies to raise it.
	ScoreBuilder *ScoreBuilder `json:"scoreBuilder,omitempty"`
}

// ScoreBuilder is the score-improvement view: a diagnosis (what's helping and
// hurting), a realistic target, and a toolkit of concrete strategies. It adapts
// to the score band — a "rebuild" plan for low scores, a "protect" plan for
// high ones — so the same block drives both the low-score (S27–S29) and
// good-score (S26) journeys.
type ScoreBuilder struct {
	// Journey is "rebuild" (< 650), "blended" (650–749), "protect" (>= 750), or
	// "unknown" (no score on file).
	Journey  string `json:"journey"`
	Headline string `json:"headline"`

	CurrentScore   *int64 `json:"currentScore"`
	TargetScoreMin int    `json:"targetScoreMin"`
	TargetScoreMax int    `json:"targetScoreMax"`
	// TimelineMonthsMin/Max bound a realistic time-to-target (0 when the plan is
	// "maintain", i.e. already at target).
	TimelineMonthsMin int `json:"timelineMonthsMin"`
	TimelineMonthsMax int `json:"timelineMonthsMax"`

	// Positives are the reassuring facts found on the file (no defaults, etc.).
	Positives []string `json:"positives"`
	// Drivers are the factors dragging the score down (weakest first).
	Drivers []ScoreDriver `json:"drivers"`
	// Strategies is the rebuild/optimize toolkit, highest-impact first.
	Strategies []BuilderStrategy `json:"strategies"`

	Disclaimer string `json:"disclaimer"`
}

// ScoreDriver is one factor pulling the score down (from the report card).
type ScoreDriver struct {
	Factor  string `json:"factor"`
	Grade   string `json:"grade"`
	Summary string `json:"summary"`
}

// BuilderStrategy is one lever in the score toolkit. EstimatedPointsMin/Max are
// nil for strategies whose payoff can't be quantified up front (e.g. disputing
// errors), which the client renders as a "check" rather than a point range.
//
// Kind distinguishes the two strategy shapes the S28 screen renders:
//   - "advice"   — behavioral guidance computed from the report (default).
//   - "product"  — an admin-curated bank offering (e.g. an FD-secured card),
//     which carries ApplyURL/RevenueNote/FD amount so the client
//     can render the hero card with a real CTA.
type BuilderStrategy struct {
	Key                string `json:"key"`
	Title              string `json:"title"`
	Detail             string `json:"detail"`
	Tag                string `json:"tag"`
	Kind               string `json:"kind"`
	EstimatedPointsMin *int   `json:"estimatedPointsMin,omitempty"`
	EstimatedPointsMax *int   `json:"estimatedPointsMax,omitempty"`

	// Product-only fields (Kind == "product"). Zero/empty otherwise.
	ApplyURL    string  `json:"applyUrl,omitempty"`
	RevenueNote string  `json:"revenueNote,omitempty"`
	FDAmount    float64 `json:"fdAmount,omitempty"`
}

// Recommendation is one actionable suggestion in the unified action plan.
type Recommendation struct {
	// Category is "score" (raise the credit score) or "interest" (cut interest).
	Category string `json:"category"`
	Title    string `json:"title"`
	Detail   string `json:"detail"`
	// Impact is a human-readable, deliberately non-promissory hint at the payoff
	// (e.g. the current factor state, or the estimated rupee saving).
	Impact string `json:"impact,omitempty"`
	// EstimatedPointsMin/Max bound the estimated score gain for "score"
	// recommendations (nil for "interest" ones). They are estimates, not
	// promises — actual movement depends on lender reporting cycles.
	EstimatedPointsMin *int `json:"estimatedPointsMin,omitempty"`
	EstimatedPointsMax *int `json:"estimatedPointsMax,omitempty"`
	Priority           int  `json:"priority"`
}

// ReportCard is the school-style graded summary of the credit profile across
// five factors, mirroring the FICO-style weight model.
type ReportCard struct {
	OverallGrade string       `json:"overallGrade"`
	Factors      []CardFactor `json:"factors"`
}

// CardFactor is one graded row in the report card.
type CardFactor struct {
	Name        string `json:"name"`        // e.g. "Payment history"
	Weight      int    `json:"weight"`      // percent weight (e.g. 35)
	Grade       string `json:"grade"`       // "A+", "A", "B", "C", "D", "F"
	Summary     string `json:"summary"`     // human-readable detail line
	Detail      string `json:"detail"`      // suggested next action
	MissedCount int64  `json:"missedCount"` // missed/delayed months (payment factor)
}

// LoanAccount is a per-tradeline summary in the insights response.
//
// InterestRatePercent and RemainingTenureMonths are lifted from the bureau
// record when present; both are 0 when the report does not carry them (the
// upstream file is often sparse on these), which downstream consumers such as
// the switch optimizer must treat as "unknown", not as a real zero.
type LoanAccount struct {
	AccountNumber string `json:"accountNumber"`
	LoanType      string `json:"loanType"`
	Company       string `json:"company"`
	// Active mirrors isActiveStatus on this tradeline's Account_Status — the same
	// test that produces ActiveAccountCount, so the per-account flag and the
	// summary count can never disagree. Clients split "current" from "closed
	// history" on this; without it every account reads as closed.
	Active                bool           `json:"active"`
	PercentagePaid        float64        `json:"percentagePaid"`
	InterestRatePercent   float64        `json:"interestRatePercent"`
	TotalTenureMonths     int64          `json:"totalTenureMonths"`
	RemainingTenureMonths int64          `json:"remainingTenureMonths"`
	CurrentBalance        float64        `json:"currentBalance"`
	OriginalLoanAmount    float64        `json:"originalLoanAmount"`
	PaymentHistory        []PaymentMonth `json:"paymentHistory"`
}

// PaymentMonth is one month's payment status in the 36-month history.
type PaymentMonth struct {
	Month    string `json:"month"`    // e.g. "2026-08"
	Status   string `json:"status"`   // "paid", "delayed", "not_reported"
	DaysLate int    `json:"daysLate"` // 0 if paid on time
}

// accountTypeMap translates Experian Account_Type codes to human-readable loan
// type names, transcribed from the "Account type master" table in section 1.9
// of the Digitap Credit Analytics spec V2.7.
//
// Keys are unpadded decimal, matching how the spec writes them; lookups go
// through loanTypeFor, which normalizes because the wire format is zero-padded
// ("05", "10").
//
// This table was previously wrong for almost every code that occurs in practice
// — 05 (the most common non-card type) read as "Two Wheeler Loan" when it is
// PERSONAL LOAN, and the label feeds models.LoanCategoryFor, so personal loans
// were also dropped from the balance-transfer optimizer for want of the word
// "personal". Check any edit against the spec table, not against intuition
// about what a number ought to mean.
var accountTypeMap = map[string]string{
	"0":  "Other",
	"1":  "Auto Loan",
	"2":  "Housing Loan",
	"3":  "Property Loan",
	"4":  "Loan Against Shares/Securities",
	"5":  "Personal Loan",
	"6":  "Consumer Loan",
	"7":  "Gold Loan",
	"8":  "Education Loan",
	"9":  "Loan to Professional",
	"10": "Credit Card",
	"11": "Leasing",
	"12": "Overdraft",
	"13": "Two-Wheeler Loan",
	"14": "Non-Funded Credit Facility",
	"15": "Loan Against Bank Deposits",
	"16": "Fleet Card",
	"17": "Commercial Vehicle Loan",
	"18": "Telco — Wireless",
	"19": "Telco — Broadband",
	"20": "Telco — Landline",
	"23": "GECL Secured",
	"24": "GECL Unsecured",
	"31": "Secured Credit Card",
	"32": "Used Car Loan",
	"33": "Construction Equipment Loan",
	"34": "Tractor Loan",
	"35": "Corporate Credit Card",
	"36": "Kisan Credit Card",
	"37": "Loan on Credit Card",
	"38": "Pradhan Mantri Jan Dhan Yojana — Overdraft",
	"39": "Mudra Loan — Shishu / Kishor / Tarun",
	"40": "Microfinance — Business Loan",
	"41": "Microfinance — Personal Loan",
	"42": "Microfinance — Housing Loan",
	"43": "Microfinance — Others",
	"44": "Pradhan Mantri Awas Yojana — CLSS",
	"45": "P2P Personal Loan",
	"46": "P2P Auto Loan",
	"47": "P2P Education Loan",
	"50": "Business Loan — Secured",
	"51": "Business Loan — General",
	"52": "Business Loan — Priority Sector — Small Business",
	"53": "Business Loan — Priority Sector — Agriculture",
	"54": "Business Loan — Priority Sector — Others",
	"55": "Business Non-Funded Credit Facility — General",
	"56": "Business Non-Funded Credit Facility — Priority Sector — Small Business",
	"57": "Business Non-Funded Credit Facility — Priority Sector — Agriculture",
	"58": "Business Non-Funded Credit Facility — Priority Sector — Others",
	"59": "Business Loan Against Bank Deposits",
	"60": "Staff Loan",
	"61": "Business Loan — Unsecured",
	"69": "Short Term Personal Loan",
	"70": "Priority Sector Gold Loan",
	"71": "Temporary Overdraft",
}

// loanTypeFor returns the human-readable loan type for an Experian
// Account_Type code, falling back to "Other" if unknown.
//
// The code arrives zero-padded on the wire ("05") while the spec's master table
// writes it plain ("5"), so normalize before looking up. Both spellings, and a
// stray surrounding space, resolve to the same entry — an unrecognized code
// silently becoming "Other" is precisely how a mismapping hides.
func loanTypeFor(accountType string) string {
	if name, ok := accountTypeMap[normalizeCode(accountType)]; ok {
		return name
	}
	return "Other"
}

// normalizeCode canonicalizes a numeric bureau code to unpadded decimal:
// " 05 " -> "5", "10" -> "10", "00" -> "0". A non-numeric or empty code is
// returned trimmed, so it simply misses the map rather than matching by luck.
func normalizeCode(code string) string {
	s := strings.TrimSpace(code)
	if s == "" {
		return s
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return s
		}
	}
	trimmed := strings.TrimLeft(s, "0")
	if trimmed == "" {
		return "0" // the code was all zeros
	}
	return trimmed
}

// Pagination bounds for ListReports.
const (
	reportDefaultSize = 20
	reportMaxSize     = 100
)

// validate checks the single client-supplied field. Returns a per-field detail
// map suitable for apperr.NewValidationWith.
func (in *CreditAnalyticsInput) validate() map[string]string {
	d := map[string]string{}
	if strings.TrimSpace(in.DeviceIP) == "" {
		d["device_ip"] = "is required"
	}
	// Optional, but bounded when present: it is stored in a VARCHAR(64) and
	// compared as an equality key, so anything longer is a 500 from the driver
	// rather than the 400 the caller deserves. The charset keeps it to something
	// safe to log and to put in an index.
	if key := strings.TrimSpace(in.IdempotencyKey); key != "" {
		if len(key) > maxIdempotencyKeyLen {
			d["idempotency_key"] = fmt.Sprintf("must be at most %d characters", maxIdempotencyKeyLen)
		} else if !idempotencyKeyFormat.MatchString(key) {
			d["idempotency_key"] = "may contain only letters, digits, '-' and '_'"
		}
	}
	return d
}

// maxIdempotencyKeyLen matches the column width; see migration 0016.
const maxIdempotencyKeyLen = 64

var idempotencyKeyFormat = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// digitapPayload is the request body posted to /credit_analytics/request. Field
// names match the upstream JSON contract exactly. The integer flags use plain
// int (not pointers) so they always serialize as 0, never get omitted.
type digitapPayload struct {
	ClientRefNum      string `json:"client_ref_num"`
	MobileNo          string `json:"mobile_no"`
	NameLookup        int    `json:"name_lookup"`
	FirstName         string `json:"first_name"`
	LastName          string `json:"last_name"`
	PAN               string `json:"pan"`
	ConsentMessage    string `json:"consent_message"`
	ConsentAcceptance string `json:"consent_acceptance"`
	DeviceType        string `json:"device_type"`
	OTP               string `json:"otp"`
	Timestamp         string `json:"timestamp"`
	DeviceIP          string `json:"device_ip"`
	ReportType        int    `json:"report_type"`
}

// Request builds the Digitap payload from the account's profile + KYC, calls
// the upstream API, persists a row capturing the request and response, then
// returns the stored row.
//
// Upstream HTTP statuses are mapped to typed app errors by classifyUpstream,
// which carries the reasoning for each:
//   - 200: success (result_code 101/102/103 all returned as a persisted row)
//   - 400: our forwarded input   -> apperr.Validation       (400)
//   - 401: our client credential -> apperr.ServiceUnavailable (503)
//   - 422: tradeline rejection   -> apperr.PanFailure       (422)
//   - anything else              -> apperr.BadGateway       (502)
//
// An unreachable provider is apperr.BadGateway too. Only 400, 402 and 422 reach
// the caller as 4xx, because only those three can be the caller's own doing.
//
// A row is persisted before any error is returned, so failed upstream calls are
// still queryable in the DB.
//
// The pull is a paid product, gated on an unspent PAID order (402 otherwise).
// The order of the three gates is load-bearing:
//
//  1. profile + PAN (buildPayload) — a user whose PAN is unverified must be told
//     that, not sent to a paywall to buy a pull that would fail the moment they
//     came back from paying for it.
//  2. entitlement — checked before the vendor call, so an unpaid caller costs us
//     nothing and gets nothing: without a purchase there is no refresh at all.
//     Past it, a report inside the reuse window is served in place of the call
//     and spends the purchase, because inside that window it is the same answer.
//  3. the pull itself — and the entitlement is SPENT only once the vendor
//     actually delivered. A pull that fails leaves the purchase unspent and
//     retryable, because the user paid for a report and did not get one; making
//     them buy a second one for our vendor's outage is indefensible.
// The bool reports that the row is a REUSED earlier report rather than a pull
// made for this request, so the caller can say so instead of implying it just
// came from the bureau. An idempotency replay does NOT set it: that is the same
// attempt arriving twice, and describing it as an older report would be wrong.
func (s *CreditAnalyticsService) Request(ctx context.Context, accountID int64, in CreditAnalyticsInput) (*models.CreditAnalyticsRequest, bool, error) {
	if details := in.validate(); len(details) > 0 {
		return nil, false, apperr.NewValidationWith("invalid credit-analytics request", details)
	}

	// Replay check, before anything else. A repeated key must answer with what
	// the first call produced, so it is settled ahead of the profile, PAN and
	// entitlement gates: re-running those could refuse a replay of a request that
	// already succeeded, purely because the account moved on since.
	key := strings.TrimSpace(in.IdempotencyKey)
	if key != "" {
		switch prior, lerr := s.repo.FindByAccountAndKey(ctx, accountID, key); {
		case lerr == nil:
			slog.Info("credit-analytics replayed: returning the first call's report",
				"account_id", accountID, "report_id", prior.ID, "idempotency_key", key)
			return prior, false, nil
		case errors.Is(lerr, repository.ErrNotFound):
			// First use of this key — carry on.
		default:
			// Fail closed, for the same reason the entitlement read does: proceeding
			// blind risks the exact double charge the key exists to prevent.
			slog.Error("credit-analytics idempotency lookup failed",
				"account_id", accountID, "error", lerr)
			return nil, false, apperr.NewServiceUnavailable(
				"We couldn't check your request. Please try again in a moment.")
		}
	}

	payload, err := s.buildPayload(ctx, accountID, in.DeviceIP)
	if err != nil {
		return nil, false, err
	}

	// Marshalled here rather than just before the upstream call: both the live
	// pull and a reused copy record it as the request that was made.
	reqBody, err := json.Marshal(payload)
	if err != nil {
		return nil, false, fmt.Errorf("marshal credit-analytics request: %w", err)
	}

	// The paywall. Until this existed the endpoint was authenticated but free:
	// the gate lived only in the app, so a token holder calling the API directly
	// got bureau reports we pay for and never bought.
	entitled, err := s.orders.HasUnspentEntitlement(ctx, accountID, models.ProductCreditAnalysis)
	if err != nil {
		// Fail closed. An entitlement we cannot read is not an entitlement we may
		// assume: the failure mode of guessing "yes" is unbounded free bureau
		// calls, and of guessing "no" is one retry for a user who really did pay.
		slog.Error("credit-analytics entitlement check failed",
			"account_id", accountID, "error", err)
		return nil, false, apperr.NewServiceUnavailable(
			"We couldn't confirm your purchase. Please try again in a moment.")
	}
	if !entitled {
		// No purchase, no refresh. Reuse is a saving on a check someone bought,
		// never a way to obtain one for free.
		slog.Info("credit-analytics refused: no unspent purchase", "account_id", accountID)
		return nil, false, apperr.NewPaymentRequired(
			"Your score check needs to be purchased before we can fetch your report.")
	}

	// The caller has bought a check. A bureau file barely moves inside the reuse
	// window, so a report from within it IS the answer a live call would give —
	// serve that and skip the vendor bill. The purchase is spent either way: what
	// it buys is a current report, not a guaranteed API call.
	//
	// It is written as a NEW row carrying the old data, not handed back as the old
	// row, so the check the user bought appears in their history like any other.
	if recent := s.reusableReport(ctx, accountID); recent != nil {
		copyRow, cerr := s.repo.CreateReusedCopy(
			ctx, recent, accountID, payload.ClientRefNum, reqBody, idempotencyPtr(key))
		if cerr != nil {
			// Falling through to the live pull is the safe failure: it costs one
			// vendor call and the caller still gets the report they paid for,
			// where returning the error would take their money for nothing.
			slog.Error("credit-analytics reuse copy failed; falling through to a live pull",
				"account_id", accountID, "source_report_id", recent.ID, "error", cerr)
		} else {
			// Count against the ORIGINAL pull, not the row we happened to copy
			// from. The newest report is itself a copy once one refresh has
			// happened, so counting the immediate source would scatter the tally
			// along the chain and disagree with reused_from_report_id, which
			// already resolves to the origin. CreateReusedCopy did that
			// resolution; reuse the answer rather than repeating it.
			countAgainst := recent.ID
			if copyRow.ReusedFromReportID != nil {
				countAgainst = *copyRow.ReusedFromReportID
			}
			if rerr := s.repo.RecordReuse(ctx, countAgainst); rerr != nil {
				slog.Warn("credit-analytics reuse not counted",
					"account_id", accountID, "report_id", countAgainst, "error", rerr)
			}
			slog.Info("credit-analytics reused: paid check served from a recent report",
				"account_id", accountID, "report_id", copyRow.ID,
				"source_report_id", recent.ID,
				"data_age", time.Since(recent.DataFetchedAt).Round(time.Minute).String())
			s.spendEntitlement(ctx, accountID, copyRow.ID)
			return copyRow, true, nil
		}
	}

	// Time the upstream call so latency is observable independently of the
	// request-scoped middleware timing.
	upstart := time.Now()
	env, httpStatus, err := s.client.Request(ctx, payload)
	upstreamLatency := time.Since(upstart).Milliseconds()
	if err != nil {
		// Network/transport failure — persist what we have, then surface the error.
		row := s.buildRow(accountID, payload, reqBody, key)
		_ = s.persist(ctx, row)
		slog.Error("credit-analytics upstream unreachable",
			"account_id", accountID,
			"client_ref_num", payload.ClientRefNum,
			"latency_ms", upstreamLatency,
			"error", err,
		)
		// We never reached Digitap — DNS, TLS, timeout, refused connection. A
		// Validation error would surface as a 400 ("Please check your details
		// and try again"), which blames the user for a network fault they have
		// no part in. BadGateway is the honest answer, and matches how the
		// sibling Digitap integration reports the same class of failure
		// (bank_statement.go).
		//
		// The raw error stays in the log above and out of the response: a Go
		// transport error can carry internal hostnames, IPs and ports, and it
		// means nothing to the person reading it on a phone.
		return nil, false, apperr.NewBadGateway(
			"Credit report service is unreachable right now. Please try again in a few minutes.")
	}

	row := s.buildRow(accountID, payload, reqBody, key)
	if env != nil {
		code := env.HTTPResponseCode
		row.HTTPStatus = &code
		row.Message = strPtr(env.Message)
		row.RequestID = strPtr(env.RequestID)
		row.ResultCode = env.ResultCode
		// env.Result is already the raw upstream JSON bytes; store them verbatim.
		if len(env.Result) > 0 {
			row.ResponseBody = env.Result
			// Lift the bureau score into its own column so the reports list can
			// return it without re-parsing the JSONB. Nil when the response
			// carries no score (failed pull / no record).
			row.CreditScore = extractBureauScore(env.Result)
		}
	}
	if persistErr := s.persist(ctx, row); persistErr != nil {
		// A concurrent call claimed this key between our lookup above and this
		// insert. Both reached Digitap — the vendor call cannot be un-made — but
		// only one row exists, so only one entitlement is spent and the caller
		// gets one consistent answer. Closing the window properly means claiming
		// the key before the upstream call, which costs an extra write on every
		// request to defend against a race the app cannot currently produce
		// (its pull is sequential). Logged at Warn so a change in that shape
		// shows up rather than staying invisible.
		if key != "" && errors.Is(persistErr, repository.ErrConflict) {
			if prior, lerr := s.repo.FindByAccountAndKey(ctx, accountID, key); lerr == nil {
				slog.Warn("credit-analytics idempotency race: duplicate upstream call, one row kept",
					"account_id", accountID, "report_id", prior.ID, "idempotency_key", key)
				return prior, false, nil
			}
		}
		return nil, false, persistErr
	}

	// The row is already persisted, so from here on the only question is what
	// to return to the caller.
	if httpStatus >= 200 && httpStatus < 300 {
		slog.Info("credit-analytics request succeeded",
			"account_id", accountID,
			"report_id", row.ID,
			"client_ref_num", payload.ClientRefNum,
			"upstream_status", httpStatus,
			"result_code", derefInt(env.ResultCode),
			"latency_ms", upstreamLatency,
		)
		// The vendor delivered, so the purchase is now spent — and only now.
		s.spendEntitlement(ctx, accountID, row.ID)

		// The response carries result_pdf: a ~1-hour URL for the generated PDF.
		// Hand it to the relay (download → encrypt → S3 → write-back) if wired.
		// Best-effort: a missing uploader, missing field, or full queue just
		// means result_pdf_url stays null; the report/score are unaffected.
		if s.pdfUploader != nil && len(env.Result) > 0 {
			if pdfURL := extractResultPDF(env.Result); pdfURL != "" {
				s.pdfUploader.Submit(accountID, row.ID, pdfURL)
			}
		}
		return row, false, nil
	}

	// env is non-nil on every path where client.Request returns a nil error,
	// but the row-building above guards it, so guard here too rather than let
	// this be the one place a malformed client could panic.
	upstreamMsg := ""
	if env != nil {
		upstreamMsg = env.Message
	}
	fail := classifyUpstream(httpStatus, upstreamMsg)
	slog.Log(ctx, fail.level, fail.logMsg,
		"account_id", accountID,
		"report_id", row.ID,
		"upstream_status", httpStatus,
		"message", upstreamMsg,
	)
	return row, false, fail.err
}

// upstreamFailure is how one non-2xx Digitap status should be reported: the
// typed error the caller returns, plus the severity and wording of the log line
// that goes with it.
type upstreamFailure struct {
	err    error
	level  slog.Level
	logMsg string
}

// classifyUpstream maps a non-2xx Digitap HTTP status to the failure to report.
//
// Split out of Request so it can be tested without standing up an account, a
// KYC record, a database and an HTTP stub — the reason this mapping went
// unnoticed while it was wrong three separate ways. The rule it encodes: only a
// status the caller could actually have caused may surface as 4xx. A vendor
// fault or a credential of ours reaching the app as 4xx renders as the user's
// own mistake, and in the 401 case as an expired session, sending them round a
// sign-in loop that cannot fix a credential held on the server.
func classifyUpstream(httpStatus int, upstreamMessage string) upstreamFailure {
	switch httpStatus {
	case http.StatusBadRequest:
		// The one genuinely user-attributable case: Digitap validates the PAN,
		// name and mobile we forwarded, all of which came from the user. Their
		// message names the offending field, so it is worth passing through.
		return upstreamFailure{
			err:    apperr.NewValidation(upstreamMessage),
			level:  slog.LevelWarn,
			logMsg: "credit-analytics upstream bad request",
		}
	case http.StatusUnauthorized:
		// Digitap rejected OUR client credentials; the caller's own session is
		// perfectly valid. Reported as a service failure the same way the PAN
		// path treats this exact cause (docs/pan-verification.md), and matching
		// the sibling Digitap integration in bank_statement.go.
		//
		// Error, not Warn: nothing a user does clears this, so it needs an
		// operator to rotate or re-provision the credential.
		return upstreamFailure{
			err:    apperr.NewServiceUnavailable("Credit report service is temporarily unavailable. Please try again later."),
			level:  slog.LevelError,
			logMsg: "credit-analytics upstream rejected our client credentials",
		}
	case http.StatusUnprocessableEntity:
		// Tradeline rejection — the bureau declined this PAN, which is about
		// the subject of the report, so 422 with the upstream wording stands.
		return upstreamFailure{
			err:    apperr.NewPanFailure(upstreamMessage),
			level:  slog.LevelWarn,
			logMsg: "credit-analytics upstream tradeline rejection",
		}
	default:
		// Everything else — a vendor 500, a 429, a proxy 504. None of them are
		// the caller's doing. The status and message stay in the log: "digitap
		// upstream error (500)" names our vendor and a status code to someone
		// who can act on neither.
		return upstreamFailure{
			err:    apperr.NewBadGateway("Credit report service returned an unexpected error. Please try again in a few minutes."),
			level:  slog.LevelError,
			logMsg: "credit-analytics upstream error",
		}
	}
}

// normalizeEmptySlices replaces every nil slice in the insights payload with an
// empty one, so it marshals as [] rather than null.
//
// This is the same defect as commit "return [] not null from empty list
// endpoints": a nil slice marshals to JSON null, which a client expecting an
// array rejects. That pass fixed the repository list destinations; these slices
// are computed here and were missed, so the bug survived in the one payload
// where it does the most damage.
//
// It is not a cosmetic difference. A strict client DTO rejects the WHOLE
// response over one null: an account with no improvable factors produced
// "drivers": null, and a 111 KB report with a perfectly good score became
// undecodable — the app showed its "couldn't load your score" state and, before
// that state existed, the paywall.
//
// Called at the very end of enrich, which is the last step of every read path
// (Request, GetReport, latest-insights) — one place to keep correct rather than
// seven. It must stay last: ScoreBuilder and Recommendations are assigned in
// enrich itself.
func normalizeEmptySlices(ins *ReportInsights) {
	if ins == nil {
		return
	}
	if ins.LoanAccounts == nil {
		ins.LoanAccounts = []LoanAccount{}
	}
	for i := range ins.LoanAccounts {
		if ins.LoanAccounts[i].PaymentHistory == nil {
			ins.LoanAccounts[i].PaymentHistory = []PaymentMonth{}
		}
	}
	if ins.Recommendations == nil {
		ins.Recommendations = []Recommendation{}
	}
	if ins.ReportCard != nil && ins.ReportCard.Factors == nil {
		ins.ReportCard.Factors = []CardFactor{}
	}
	if sb := ins.ScoreBuilder; sb != nil {
		if sb.Positives == nil {
			sb.Positives = []string{}
		}
		if sb.Drivers == nil {
			sb.Drivers = []ScoreDriver{}
		}
		if sb.Strategies == nil {
			sb.Strategies = []BuilderStrategy{}
		}
	}
}

// ReportInsightsFromRow derives the analytics view from an already-persisted
// report row (e.g. the one just written by Request) and enriches it with the
// interest-reduction opportunities, recommendations, and score-builder block.
// It mirrors GetReport but skips the DB lookup, so a freshly-generated report
// can be returned as insights in the same call that created it.
func (s *CreditAnalyticsService) ReportInsightsFromRow(ctx context.Context, row *models.CreditAnalyticsRequest) (*ReportInsights, error) {
	insights, err := s.insightsFromRow(row)
	if err != nil {
		return nil, err
	}
	s.enrich(ctx, insights)
	return insights, nil
}

// buildPayload assembles the Digitap request from the account's profile and KYC
// record, generating the per-request correlation id, OTP, and timestamp.
func (s *CreditAnalyticsService) buildPayload(ctx context.Context, accountID int64, deviceIP string) (*digitapPayload, error) {
	acc, err := s.accounts.FindByID(ctx, accountID)
	if err != nil {
		return nil, apperr.NewNotFound("Account not found")
	}

	// The profile step must be complete: mobile + name are mandatory upstream
	// inputs and only land on the account via PUT /api/profile.
	missing := map[string]string{}
	if acc.PrimaryPhone == nil || strings.TrimSpace(*acc.PrimaryPhone) == "" {
		missing["mobile_no"] = "account profile has no mobile number; complete your profile"
	}
	if acc.FirstName == nil || strings.TrimSpace(*acc.FirstName) == "" {
		missing["first_name"] = "account profile has no first name; complete your profile"
	}
	if acc.LastName == nil || strings.TrimSpace(*acc.LastName) == "" {
		missing["last_name"] = "account profile has no last name; complete your profile"
	}
	if len(missing) > 0 {
		// Debug: these are routine client omissions, not faults. Keys only,
		// never the values (mobile/name are PII).
		slog.Debug("credit-analytics rejected: incomplete profile",
			"account_id", accountID,
			"missing", keysOf(missing),
		)
		return nil, apperr.NewValidationWith("invalid credit-analytics request", missing)
	}

	// PAN must already be on file AND verified. Verification is an admin action
	// (POST /api/admin/kyc/pan/{id}/verify); an unverified PAN cannot gate the
	// credit-analytics upstream.
	kyc, err := s.accounts.FindKYCByAccount(ctx, accountID)
	if err != nil {
		return nil, apperr.NewValidationWith("invalid credit-analytics request",
			map[string]string{"pan": "no PAN on file; submit one via POST /api/kyc/pan"})
	}
	if strings.TrimSpace(kyc.PANNumber) == "" {
		return nil, apperr.NewValidationWith("invalid credit-analytics request",
			map[string]string{"pan": "no PAN on file; submit one via POST /api/kyc/pan"})
	}
	if !kyc.PANVerified {
		slog.Debug("credit-analytics rejected: PAN not verified", "account_id", accountID)
		return nil, apperr.NewValidationWith("invalid credit-analytics request",
			map[string]string{"pan": "PAN not verified; an admin must verify it before requesting credit analytics"})
	}

	otp, err := generateOTP(otpDigits)
	if err != nil {
		return nil, fmt.Errorf("generate otp: %w", err)
	}

	return &digitapPayload{
		ClientRefNum:      generateClientRefNum(),
		MobileNo:          *acc.PrimaryPhone,
		FirstName:         *acc.FirstName,
		LastName:          *acc.LastName,
		PAN:               kyc.PANNumber,
		NameLookup:        nameLookupOff,
		ConsentMessage:    consentMessage,
		ConsentAcceptance: consentYes,
		DeviceType:        deviceTypeWeb,
		OTP:               otp,
		Timestamp:         time.Now().UTC().Format(timestampLayout),
		DeviceIP:          deviceIP,
		ReportType:        reportTypeFlag,
	}, nil
}

// buildRow assembles a model row from the assembled payload (before the upstream
// response fields are filled in by the caller).
func (s *CreditAnalyticsService) buildRow(accountID int64, p *digitapPayload, reqBody []byte, key string) *models.CreditAnalyticsRequest {
	var idem *string
	if key != "" {
		idem = &key
	}
	return &models.CreditAnalyticsRequest{
		AccountID:      &accountID,
		IdempotencyKey: idem,
		ClientRefNum: p.ClientRefNum,
		MobileNo:     p.MobileNo,
		RequestBody:  reqBody,
	}
}

func (s *CreditAnalyticsService) persist(ctx context.Context, row *models.CreditAnalyticsRequest) error {
	if err := s.repo.Create(ctx, row); err != nil {
		return fmt.Errorf("persist credit-analytics request: %w", err)
	}
	return nil
}

// derefInt makes a *int loggable.
//
// slog renders a pointer as its address, so "result_code", env.ResultCode put
// something like 0x1a03f1de0838 on every successful pull where Digitap's 101
// belonged — a field that looked populated, survived review, and told an
// operator nothing. The result code is the difference between a record found
// (101) and a provider gap (102/103), which is exactly what you go to that log
// line to learn.
//
// Returns nil rather than a sentinel for a nil pointer: "result_code=<nil>" is
// honest about the upstream having sent none, where 0 would be a code Digitap
// can never return and reads as data.
func derefInt(p *int) any {
	if p == nil {
		return nil
	}
	return *p
}

// spendEntitlement claims the caller's oldest unspent purchase against the
// report it bought.
//
// Best-effort by design: the report exists and the caller is entitled to see it,
// so a bookkeeping failure must never turn a delivered report into an error. It
// leaves the purchase unspent, which errs toward the user — they can run one
// more check — and is loud in the log.
//
// Called from both paths that hand a report back to a paying caller: a live pull
// once the vendor has delivered, and a reuse that satisfied the same purchase
// without one. One definition, so the two can never drift into charging
// differently for the same thing.
func (s *CreditAnalyticsService) spendEntitlement(ctx context.Context, accountID, reportID int64) {
	spent, err := s.orders.SpendEntitlement(ctx, accountID, models.ProductCreditAnalysis, reportID)
	if err != nil {
		slog.Error("credit-analytics entitlement not spent: update failed",
			"account_id", accountID, "report_id", reportID, "error", err)
		return
	}
	if !spent {
		// Nothing was claimable although the gate passed. A concurrent request
		// took the last order between the two statements — rare, and worth a
		// line, because the alternative reading is a hole in the gate.
		slog.Warn("credit-analytics entitlement not spent: none left to claim",
			"account_id", accountID, "report_id", reportID)
	}
}

// reusableReport returns the account's most recent successful report when it is
// young enough to stand in for a fresh pull, or nil.
//
// Consulted ONLY after the entitlement gate has passed: reuse is a saving on a
// check the caller has bought, never a way to obtain one for free.
//
// The premise is that a credit file barely moves inside this window, so the
// stored report is what a live call would return. The purchase is therefore spent
// on it: what a check buys is a current report, not a guaranteed round-trip to
// the bureau. What the user is spared is the wait; what we are spared is the
// vendor bill.
//
// The consequence to keep in view: paying twice inside the window returns the
// same report twice, and spends both. That is accepted — the data genuinely has
// not changed — but it means nothing on this path stops a user buying something
// they already have. If that needs preventing, prevent it before the payment,
// not here.
//
// Reuse is invisible to the user: the app runs the same flow whichever it got.
//
// Fails OPEN: a lookup error returns nil, so the caller carries on to the live
// pull. Unlike the entitlement read, which guards money going out and must
// refuse when unsure, this one only decides whether work can be skipped.
func (s *CreditAnalyticsService) reusableReport(ctx context.Context, accountID int64) *models.CreditAnalyticsRequest {
	if s.reuseWindow <= 0 {
		return nil
	}
	recent, err := s.repo.FindLatestByAccount(ctx, accountID)
	if err != nil {
		if !errors.Is(err, repository.ErrNotFound) {
			slog.Warn("credit-analytics reuse lookup failed; treating as no reusable report",
				"account_id", accountID, "error", err)
		}
		return nil
	}
	// Measured against when the DATA was fetched, never when the row was written.
	// A copy inherits data_fetched_at, so serving copies can never keep the window
	// open — which is what would happen if this read CreatedAt and every refresh
	// minted a row that looked brand new.
	if time.Since(recent.DataFetchedAt) >= s.reuseWindow {
		return nil
	}
	return recent
}

// idempotencyPtr renders the caller's key for storage: empty means they sent
// none, which the column records as NULL rather than as an empty string that
// would collide with the next keyless caller under the unique index.
func idempotencyPtr(key string) *string {
	if key == "" {
		return nil
	}
	return &key
}

// ListReports returns one page of the caller's reports, newest first. Each
// item exposes only the report id and generation date, per the API contract.
func (s *CreditAnalyticsService) ListReports(ctx context.Context, accountID int64, page, size int) (*ReportPage, error) {
	if page < 1 {
		page = 1
	}
	if size < 1 {
		size = reportDefaultSize
	}
	if size > reportMaxSize {
		size = reportMaxSize
	}
	offset := (page - 1) * size

	rows, err := s.repo.FindByAccountPaged(ctx, accountID, size, offset)
	if err != nil {
		return nil, err
	}
	total, err := s.repo.CountByAccount(ctx, accountID)
	if err != nil {
		return nil, err
	}

	items := make([]ReportSummary, 0, len(rows))
	for _, r := range rows {
		items = append(items, ReportSummary{ID: r.ID, CreatedAt: r.CreatedAt, CreditScore: r.CreditScore})
	}
	return &ReportPage{Items: items, Page: page, Size: size, Total: total}, nil
}

// GetReport returns the derived credit analytics for one of the caller's own
// reports (looked up by id), rather than the raw Digitap response. A row
// belonging to another account is reported as not found (no existence leak).
func (s *CreditAnalyticsService) GetReport(ctx context.Context, accountID, id int64) (*ReportInsights, error) {
	row, err := s.findOwnedReport(ctx, accountID, id)
	if err != nil {
		return nil, err
	}
	insights, err := s.insightsFromRow(row)
	if err != nil {
		return nil, err
	}
	s.enrich(ctx, insights)
	return insights, nil
}

// GetReportRaw returns the caller's own report row verbatim, including the
// stored raw Digitap response body. A row belonging to another account is
// reported as not found (no existence leak).
func (s *CreditAnalyticsService) GetReportRaw(ctx context.Context, accountID, id int64) (*models.CreditAnalyticsRequest, error) {
	return s.findOwnedReport(ctx, accountID, id)
}

// findOwnedReport fetches a report row and enforces that it belongs to the
// caller, mapping a missing or foreign row to the same NotFound so ownership
// can't be probed by id.
func (s *CreditAnalyticsService) findOwnedReport(ctx context.Context, accountID, id int64) (*models.CreditAnalyticsRequest, error) {
	row, err := s.repo.FindByID(ctx, id)
	if errors.Is(err, repository.ErrNotFound) {
		return nil, apperr.NewNotFound("Report not found")
	}
	if err != nil {
		return nil, err
	}
	// Ownership check: account_id is nullable in the schema but always set by
	// the request flow, so a nil here is treated as not-owned.
	if row.AccountID == nil || *row.AccountID != accountID {
		return nil, apperr.NewNotFound("Report not found")
	}
	return row, nil
}

// GetLatestReportInsights returns derived credit analytics from the account's
// most recent successful report: the bureau score, on-time payment %, card
// utilization %, and enquiry count for the past 180 days. Returns ErrNotFound
// if no successful report exists yet.
func (s *CreditAnalyticsService) GetLatestReportInsights(ctx context.Context, accountID int64) (*ReportInsights, error) {
	row, err := s.repo.FindLatestByAccount(ctx, accountID)
	if errors.Is(err, repository.ErrNotFound) {
		return nil, apperr.NewNotFound("No credit report found")
	}
	if err != nil {
		return nil, err
	}
	insights, err := s.insightsFromRow(row)
	if err != nil {
		return nil, err
	}
	s.enrich(ctx, insights)
	return insights, nil
}

// enrich augments a parsed report with the interest-reduction opportunities and
// the unified recommendation list, so a single analytics call gives the client
// both levers: raise the score and cut interest. Enrichment is best-effort — a
// failure to load providers/settings leaves interestSavings nil rather than
// failing the whole insights response.
func (s *CreditAnalyticsService) enrich(ctx context.Context, insights *ReportInsights) {
	if insights == nil {
		return
	}
	if s.loanSwitch != nil && len(insights.LoanAccounts) > 0 {
		if opps, err := s.loanSwitch.OpportunitiesFromInsights(ctx, insights); err != nil {
			slog.Warn("insights enrichment: loan-switch opportunities failed",
				"report_id", insights.ReportID, "error", err)
		} else {
			insights.InterestSavings = opps
		}
	}
	// Bank offerings for the score-builder toolkit (S28). Best-effort: if the
	// store is unavailable the toolkit falls back to generic advice.
	var offerings []models.BankOffering
	if s.scoreBuilder != nil && insights.CreditScore != nil {
		if offs, err := s.scoreBuilder.OfferingsForScore(ctx, int(*insights.CreditScore)); err != nil {
			slog.Warn("insights enrichment: bank offerings failed",
				"report_id", insights.ReportID, "error", err)
		} else {
			offerings = offs
		}
	}
	insights.Recommendations = buildRecommendations(insights)
	insights.ScoreBuilder = buildScoreBuilder(insights, offerings)

	// Last step on every read path, and deliberately last: ScoreBuilder is
	// attached just above, so a guard placed any earlier cannot see its slices —
	// which is exactly how "drivers": null survived a first attempt at this fix.
	normalizeEmptySlices(insights)
}

// buildRecommendations flattens the two improvement levers into one prioritized
// list: concrete interest savings first (biggest net saving first), then the
// score factors that have room to improve (weakest grade first). It is
// nil-safe: a report with no card and no switch data yields an empty list.
func buildRecommendations(ins *ReportInsights) []Recommendation {
	recs := []Recommendation{}

	// ---- Interest reductions (from recommended switches) ----
	if ins.InterestSavings != nil {
		recommended := make([]SwitchOpportunity, 0, len(ins.InterestSavings.Opportunities))
		for _, o := range ins.InterestSavings.Opportunities {
			if o.Recommended && o.BestProvider != nil {
				recommended = append(recommended, o)
			}
		}
		sort.SliceStable(recommended, func(i, j int) bool {
			return recommended[i].NetSaving > recommended[j].NetSaving
		})
		for _, o := range recommended {
			impact := fmt.Sprintf("Save about ₹%.0f over the tenure (₹%.0f/month)", o.NetSaving, o.MonthlyEmiSaving)
			if o.RecoveryMonths != nil {
				impact += fmt.Sprintf("; switching cost recovered in ~%d month(s)", *o.RecoveryMonths)
			}
			recs = append(recs, Recommendation{
				Category: "interest",
				Title:    "Refinance your " + strings.ToLower(o.LoanType) + " loan",
				Detail: fmt.Sprintf("Move from %s at %.2f%% to %s at %.2f%%.",
					o.CurrentLender, o.CurrentRatePercent, o.BestProvider.Name, o.NewRatePercent),
				Impact: impact,
			})
		}
	}

	// ---- Score improvements (from report-card factors below an A) ----
	if ins.ReportCard != nil {
		weak := make([]CardFactor, 0, len(ins.ReportCard.Factors))
		for _, f := range ins.ReportCard.Factors {
			if gradeImprovable(f.Grade) {
				weak = append(weak, f)
			}
		}
		sort.SliceStable(weak, func(i, j int) bool {
			return gradeRank(weak[i].Grade) < gradeRank(weak[j].Grade) // weakest first
		})
		for _, f := range weak {
			rec := Recommendation{
				Category: "score",
				Title:    "Improve " + strings.ToLower(f.Name),
				Detail:   f.Detail,
				Impact:   f.Summary,
			}
			if lo, hi, ok := estimatedScoreGain(f.Name, f.Grade); ok {
				rec.EstimatedPointsMin = &lo
				rec.EstimatedPointsMax = &hi
				rec.Impact = fmt.Sprintf("Estimated +%d–%d pts · %s", lo, hi, f.Summary)
			}
			recs = append(recs, rec)
		}
	}

	for i := range recs {
		recs[i].Priority = i + 1
	}
	return recs
}

// gradeRank orders letter grades worst-to-best for prioritization.
func gradeRank(grade string) int {
	switch grade {
	case "F":
		return 0
	case "D":
		return 1
	case "C":
		return 2
	case "B":
		return 3
	case "A":
		return 4
	default: // "A+"
		return 5
	}
}

// gradeImprovable reports whether a factor grade has meaningful room to improve
// (anything below an A).
func gradeImprovable(grade string) bool { return gradeRank(grade) < 4 }

// estimatedScoreGain returns an ESTIMATED point-range (low, high) for fixing a
// report-card factor, scaled by how weak it currently is. These are indicative
// ranges — the compliance-mandated "estimated" qualifier is added by the caller
// — matched to the Score Improvement Playbook levers in the design (S28/S29).
// ok is false for factors with no fast, quantifiable lever (nothing is fabricated).
func estimatedScoreGain(factorName, grade string) (low, high int, ok bool) {
	rank := gradeRank(grade) // 0=F .. 3=B (only called for below-A factors)
	switch factorName {
	case "Payment history":
		// Recovering from missed payments — the heaviest factor (35%).
		switch {
		case rank <= 1: // F/D
			return 50, 80, true
		case rank == 2: // C
			return 30, 50, true
		default: // B
			return 15, 30, true
		}
	case "Credit utilisation":
		// The fastest lever: paying balances down reports quickly (30%).
		switch {
		case rank <= 1: // D/F — maxed out
			return 40, 60, true
		case rank == 2: // C
			return 30, 50, true
		default: // B
			return 15, 30, true
		}
	case "Enquiries":
		// Enquiries age off after ~6 clean months.
		if rank <= 2 {
			return 20, 40, true
		}
		return 10, 20, true
	case "Credit age", "Credit mix":
		// Slow, structural factors — modest, time-driven gains.
		return 5, 15, true
	default:
		return 0, 0, false
	}
}

// isDerogatoryStatus reports whether a Written_off_Settled_Status flag marks a
// serious negative. Blank / "?" / "0" mean none.
func isDerogatoryStatus(s string) bool {
	switch strings.TrimSpace(s) {
	case "", "?", "0", "00":
		return false
	default:
		return true
	}
}

const scoreBuilderDisclaimer = "Point impacts and timelines are estimates from your file, not guarantees. Real movement depends on lender reporting cycles."

// buildScoreBuilder produces the score-improvement diagnosis + toolkit
// (Journey 05·C for low scores, and a protect plan for high ones). It is
// nil-safe on a report with no card. Nothing is fabricated: positives and
// strategies are gated on what the file actually shows.
//
// offerings carries the admin-curated bank products whose score band contains
// the user's score; when non-empty the rebuild toolkit emits one product
// strategy per offering (the S28 hero card) instead of the generic FD-card text.
func buildScoreBuilder(ins *ReportInsights, offerings []models.BankOffering) *ScoreBuilder {
	if ins.ReportCard == nil {
		return nil
	}
	sb := &ScoreBuilder{CurrentScore: ins.CreditScore, Disclaimer: scoreBuilderDisclaimer}

	var score int
	if ins.CreditScore != nil {
		score = int(*ins.CreditScore)
	}
	switch {
	case score <= 0:
		sb.Journey, sb.Headline = "unknown", "Score unavailable — no plan yet"
	case score < 650:
		sb.Journey, sb.Headline = "rebuild", "Needs work — but fixable"
		sb.TargetScoreMin, sb.TargetScoreMax = 700, max(700, min(750, score+100))
		sb.TimelineMonthsMin, sb.TimelineMonthsMax = 8, 12
	case score < 750:
		sb.Journey, sb.Headline = "blended", "Good — with clear room to grow"
		sb.TargetScoreMin, sb.TargetScoreMax = 750, max(750, min(800, score+70))
		sb.TimelineMonthsMin, sb.TimelineMonthsMax = 6, 9
	default:
		sb.Journey, sb.Headline = "protect", "Excellent — protect and profit"
		sb.TargetScoreMin, sb.TargetScoreMax = max(800, score), 900
		// Already at target: the plan is to maintain, so no timeline.
	}

	// ---- Positives (the reassuring "good news") ----
	if ins.DerogatoryAccounts == 0 {
		sb.Positives = append(sb.Positives, "No defaults, write-offs, or settlements on your file.")
	}
	// No claim either way when the figure is unknown: "most payments are on time"
	// is not something to tell someone whose lenders have reported no payments.
	if pct := ins.OnTimePaymentPercent; pct != nil {
		switch {
		case *pct >= 95:
			sb.Positives = append(sb.Positives, "Strong repayment record — nearly all payments on time.")
		case *pct >= 80:
			sb.Positives = append(sb.Positives, "Most payments are on time — a solid base to build on.")
		}
	}
	if ins.EnquiryCount180Days == 0 {
		sb.Positives = append(sb.Positives, "No recent enquiries — lenders see no credit hunger.")
	}

	// ---- Drivers (what's pulling the score down) ----
	for _, f := range ins.ReportCard.Factors {
		if gradeImprovable(f.Grade) {
			sb.Drivers = append(sb.Drivers, ScoreDriver{Factor: f.Name, Grade: f.Grade, Summary: f.Summary})
		}
	}
	sort.SliceStable(sb.Drivers, func(i, j int) bool {
		return gradeRank(sb.Drivers[i].Grade) < gradeRank(sb.Drivers[j].Grade)
	})

	// ---- Strategies (the toolkit) ----
	sb.Strategies = buildStrategies(ins, sb.Journey, offerings)
	return sb
}

// buildStrategies assembles the score toolkit from the report's signals, then
// orders it highest-estimated-impact first (unquantified levers like disputing
// errors sort last).
//
// offerings are the admin-curated bank products for the user's score. On the
// rebuild journey, each becomes a "product" strategy (the S28 hero card with a
// real bank name, apply CTA, and FD amount). When none are configured the
// toolkit falls back to one generic FD-card advice entry so the lever is still
// surfaced.
func buildStrategies(ins *ReportInsights, journey string, offerings []models.BankOffering) []BuilderStrategy {
	strat := func(key, title, detail, tag string, lo, hi int) BuilderStrategy {
		s := BuilderStrategy{Key: key, Title: title, Detail: detail, Tag: tag, Kind: "advice"}
		if hi > 0 {
			l, h := lo, hi
			s.EstimatedPointsMin, s.EstimatedPointsMax = &l, &h
		}
		return s
	}

	var out []BuilderStrategy

	if ins.CardUtilizationPercent > 30 {
		out = append(out, strat("crush_utilisation", "Crush card utilisation below 30%",
			fmt.Sprintf("Your revolving usage is %.0f%%. Getting it under 30%% (ideally under 10%%) is the fastest score lever you control.", ins.CardUtilizationPercent),
			"FASTEST FIX", 30, 50))
	}

	var missed int64
	for _, f := range ins.ReportCard.Factors {
		if f.Name == "Payment history" {
			missed = f.MissedCount
		}
	}
	if journey == "rebuild" || missed > 0 {
		out = append(out, strat("perfect_streak", "Build a 12-month on-time streak",
			"Autopay every EMI and card bill. Late marks fade as clean months stack on top — payment history is the heaviest scoring factor.",
			"HIGH IMPACT", 50, 80))
	}

	if ins.EnquiryCount180Days > 2 {
		out = append(out, strat("application_freeze", "Freeze new applications for 6 months",
			fmt.Sprintf("You have %d enquiries in the last 6 months. Each one dents the score; they stop hurting after ~6 clean months.", ins.EnquiryCount180Days),
			"PROTECT", 20, 40))
	}

	// ---- FD-secured card: curated bank offerings, else generic advice ----
	if journey == "rebuild" {
		if len(offerings) > 0 {
			for _, o := range offerings {
				out = append(out, bankOfferingStrategy(o))
			}
		} else {
			// No products curated — keep the generic lever so the toolkit still
			// surfaces the strategy, just without a named bank / CTA.
			out = append(out, strat("fd_secured_card", "Open an FD-secured credit card",
				"Open a small fixed deposit and get a card against it — guaranteed approval, no hard enquiry, and the FD keeps earning interest while every on-time month builds positive history.",
				"BEST FOR YOU", 40, 80))
		}
	}

	if journey == "protect" {
		out = append(out, strat("protect_streak", "Autopay every bill to protect your streak",
			"At your level the wins are protective: one 30-day slip can cost 50–100 points. Autopay locks in the streak that anchors your score.",
			"PROTECT", 0, 0))
		out = append(out, strat("premium_card", "Claim a lifetime-free premium card",
			"A clean high-score file qualifies for lifetime-free premium cards — more limit (lower utilisation) and rewards, with no joining fee.",
			"PERK", 0, 0))
	}

	// Always worth a look; the payoff can't be quantified up front.
	out = append(out, strat("dispute_errors", "Dispute any report errors",
		"Review your report for wrong late-marks or accounts you don't recognise and dispute them with the bureau — corrections are free and can lift the score quickly.",
		"CHECK", 0, 0))

	sort.SliceStable(out, func(i, j int) bool {
		return strategyMax(out[i]) > strategyMax(out[j])
	})
	return out
}

// bankOfferingStrategy turns one curated offering into a "product" strategy —
// the S28 hero card: a named bank, the FD it sits against, an apply CTA, the
// estimated point range, and the ops revenue note.
func bankOfferingStrategy(o models.BankOffering) BuilderStrategy {
	lo, hi := o.EstimatedPointsMin, o.EstimatedPointsMax
	detail := "Open a fixed deposit and get a card against it — no approval risk, no enquiry. Spend little, autopay in full. The FD keeps earning while every month adds positive history."
	if o.MinFDAmount > 0 {
		detail = fmt.Sprintf("Open a ₹%.0f FD → get the %s against it (no approval risk, no enquiry). Spend little, autopay in full. The FD keeps earning while every month adds positive history.", o.MinFDAmount, o.Name)
	}
	s := BuilderStrategy{
		Key: "fd_secured_card", Title: o.Name, Detail: detail,
		Tag: "BEST FOR YOU", Kind: "product",
		ApplyURL: o.ApplyURL, RevenueNote: o.RevenueNote, FDAmount: o.MinFDAmount,
	}
	if hi > 0 {
		s.EstimatedPointsMin = &lo
		s.EstimatedPointsMax = &hi
	}
	return s
}

func strategyMax(s BuilderStrategy) int {
	if s.EstimatedPointsMax == nil {
		return 0
	}
	return *s.EstimatedPointsMax
}

// insightsFromRow derives the analytics view of a stored report row. See
// insightsFromReportRow for the details; this method just delegates so callers
// on the service read naturally.
func (s *CreditAnalyticsService) insightsFromRow(row *models.CreditAnalyticsRequest) (*ReportInsights, error) {
	return insightsFromReportRow(row)
}

// insightsFromReportRow derives the analytics view of a stored report row. The
// bureau payload is parsed when present; a row without a stored response (a
// failed pull / no-record result) still yields the metadata fields (report id,
// score, outdated) with zeroed analytics rather than an error. The score
// prefers the persisted column and falls back to parsing the response so
// reports created before the column existed still report a score. Shared by the
// analytics and loan-switch services.
func insightsFromReportRow(row *models.CreditAnalyticsRequest) (*ReportInsights, error) {
	insights := &ReportInsights{}
	if len(row.ResponseBody) > 0 {
		parsed, err := parseReportInsights(row.ResponseBody)
		if err != nil {
			return nil, err
		}
		insights = parsed
	}
	insights.ReportID = row.ID
	insights.CreditScore = row.CreditScore
	if insights.CreditScore == nil {
		insights.CreditScore = extractBureauScore(row.ResponseBody)
	}
	insights.CreatedAt = row.CreatedAt
	insights.Outdated = time.Since(row.CreatedAt) > reportFreshWindow
	return insights, nil
}

// extractBureauScore lifts SCORE.BureauScore out of the raw Digitap
// INProfileResponse envelope for cheap per-row storage. It returns nil when the
// payload is empty, unparseable, or carries no numeric score (e.g. a failed
// pull or a no-record response), so a missing score is never stored as 0.
func extractBureauScore(raw json.RawMessage) *int64 {
	raw = unwrapResultObject(raw)
	if len(raw) == 0 {
		return nil
	}
	var wrapper struct {
		ResultJSON struct {
			INProfileResponse struct {
				SCORE struct {
					BureauScore string `json:"BureauScore"`
				} `json:"SCORE"`
			} `json:"INProfileResponse"`
		} `json:"result_json"`
	}
	if err := json.Unmarshal(raw, &wrapper); err != nil {
		return nil
	}
	s := strings.TrimSpace(wrapper.ResultJSON.INProfileResponse.SCORE.BureauScore)
	if s == "" {
		return nil
	}
	score := atoiSafe64(s)
	if score <= 0 {
		return nil
	}
	return &score
}

// extractResultPDF lifts the result_pdf URL out of the Digitap response. When
// the request asks for a PDF (reportTypeFlag), Digitap returns result_pdf: a URL
// for the generated PDF valid for ~1 hour. The field's exact nesting in the
// envelope isn't documented, so this checks the likely locations defensively
// and returns "" when not found
// (the caller treats that as "no PDF to relay" — best-effort, never an error).
//
// Checked locations, in order:
//  1. Top-level of the stored result object: {"result_pdf": "...", "result_json": {...}}
//  2. Alongside the bureau payload, under INProfileResponse: {"result_json":{"INProfileResponse":{"result_pdf":"..."}}}
//  3. The raw envelope itself (in case env.Result is the full envelope): {"result":{"result_pdf":"..."}}
func extractResultPDF(raw json.RawMessage) string {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return ""
	}

	// 1. Top-level of the result object.
	if s := pdfFromObject(trimmed); s != "" {
		return s
	}

	// 2. Nested under result_json.INProfileResponse.
	inner := unwrapResultObject(raw)
	if s := pdfFromINProfile(inner); s != "" {
		return s
	}

	// 3. Full envelope: unwrap once more and retry the top-level check.
	var envelope struct {
		Result json.RawMessage `json:"result"`
	}
	if json.Unmarshal(trimmed, &envelope) == nil && len(envelope.Result) > 0 {
		if s := pdfFromObject(bytes.TrimSpace(envelope.Result)); s != "" {
			return s
		}
	}
	return ""
}

// pdfFromObject reads a top-level result_pdf string from a JSON object.
func pdfFromObject(raw []byte) string {
	var v struct {
		ResultPDF string `json:"result_pdf"`
	}
	if json.Unmarshal(raw, &v) != nil {
		return ""
	}
	return strings.TrimSpace(v.ResultPDF)
}

// pdfFromINProfile reads result_pdf from result_json.INProfileResponse.
func pdfFromINProfile(raw json.RawMessage) string {
	var v struct {
		ResultJSON struct {
			INProfileResponse struct {
				ResultPDF string `json:"result_pdf"`
			} `json:"INProfileResponse"`
		} `json:"result_json"`
	}
	if json.Unmarshal(raw, &v) != nil {
		return ""
	}
	return strings.TrimSpace(v.ResultJSON.INProfileResponse.ResultPDF)
}

// unwrapResultObject normalizes a stored Digitap payload to the inner "result"
// object — the one whose top-level key is "result_json" — which is what the
// service persists (env.Result) and what parseReportInsights/extractBureauScore
// expect.
//
// It tolerates the easy-to-make mistake of storing the FULL upstream envelope
// instead (e.g. via a seed SQL): {"http_response_code":..., "result":{"result_json":{...}}}.
// The envelope has no top-level "result_json", so without unwrapping the parser
// silently matches an empty struct and every body-derived field reads as zero
// — a trap that looks like a parser bug. When the input is already the inner
// object (has a top-level "result_json", or no "result" key at all) it is
// returned unchanged.
func unwrapResultObject(raw json.RawMessage) json.RawMessage {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return raw
	}
	var probe struct {
		ResultJSON json.RawMessage `json:"result_json"`
		Result     json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(trimmed, &probe); err != nil {
		return raw // not even JSON — let the caller fail on it
	}
	if len(probe.ResultJSON) > 0 {
		return raw // already the inner object (has top-level result_json)
	}
	if len(probe.Result) > 0 {
		return probe.Result // envelope stored by mistake — unwrap one level
	}
	return raw
}

// caisAccountDetail is one tradeline from CAIS_Account_DETAILS.
type caisAccountDetail struct {
	PaymentHistoryProfile string `json:"Payment_History_Profile"`
	// CAISAccountHistory is the per-month record, and the better source: it
	// carries an explicit year and month plus the exact days past due, where
	// Payment_History_Profile is a positional string that has to be dated by
	// counting backwards and only encodes a DPD bucket. It also runs one month
	// longer in practice — see paymentHistoryFor.
	CAISAccountHistory            caisAccountHistoryList `json:"CAIS_Account_History"`
	PortfolioType                 string                 `json:"Portfolio_Type"`
	CreditLimitAmount             string                 `json:"Credit_Limit_Amount"`
	CurrentBalance                string                 `json:"Current_Balance"`
	AccountStatus                 string                 `json:"Account_Status"`
	ScheduledMonthlyPaymentAmount string                 `json:"Scheduled_Monthly_Payment_Amount"`
	RateOfInterest                string                 `json:"Rate_of_Interest"`
	AccountType                   string                 `json:"Account_Type"`
	AccountNumber                 string                 `json:"Account_Number"`
	SubscriberName                string                 `json:"Subscriber_Name"`
	OpenDate                      string                 `json:"Open_Date"`
	HighestCredit                 string                 `json:"Highest_Credit_or_Original_Loan_Amount"`
	RepaymentTenure               string                 `json:"Repayment_Tenure"`
	WrittenOffSettledStatus       string                 `json:"Written_off_Settled_Status"`
}

// caisAccountDetailList tolerates a real Digitap/Experian quirk: CAIS_Account_DETAILS
// is a JSON array when there are multiple tradelines, but the XML→JSON
// conversion collapses it to a single JSON object when there is exactly one.
// UnmarshalJSON accepts either shape and always yields a slice, so a one-loan
// report is no longer silently dropped (or, worse, an unmarshal error).
// caisAccountHistoryEntry is one month of CAIS_Account_History.
//
// Every field arrives as a zero-padded STRING ("2026", "08", "000"), not a
// number. Days_Past_Due is the authority here: Asset_Classification is not
// consistent between sources — real reports send "?" where the captured
// fixtures send "STD" — so it is only consulted when the DPD is unusable.
type caisAccountHistoryEntry struct {
	Year                string `json:"Year"`
	Month               string `json:"Month"`
	DaysPastDue         string `json:"Days_Past_Due"`
	AssetClassification string `json:"Asset_Classification"`
}

// caisAccountHistoryList tolerates the same XML->JSON collapse as
// caisAccountDetailList: an array of months becomes a bare object when there is
// exactly one.
type caisAccountHistoryList []caisAccountHistoryEntry

func (l *caisAccountHistoryList) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil
	}
	if trimmed[0] == '[' {
		var arr []caisAccountHistoryEntry
		if err := json.Unmarshal(trimmed, &arr); err != nil {
			return err
		}
		*l = arr
		return nil
	}
	var one caisAccountHistoryEntry
	if err := json.Unmarshal(trimmed, &one); err != nil {
		return err
	}
	*l = caisAccountHistoryList{one}
	return nil
}

type caisAccountDetailList []caisAccountDetail

func (l *caisAccountDetailList) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil
	}
	if trimmed[0] == '[' {
		var arr []caisAccountDetail
		if err := json.Unmarshal(trimmed, &arr); err != nil {
			return err
		}
		*l = arr
		return nil
	}
	// A single object (one tradeline) — wrap it into a one-element slice.
	var one caisAccountDetail
	if err := json.Unmarshal(trimmed, &one); err != nil {
		return err
	}
	*l = caisAccountDetailList{one}
	return nil
}

// parseReportInsights extracts on-time payment %, card utilization %, and
// 180-day enquiry count from the raw Digitap INProfileResponse JSONB.
//
// Response shape (from Digitap V2.7):
//
//	{
//	  "result_json": {
//	    "INProfileResponse": {
//	      "CAIS_Account": {
//	        "CAIS_Account_DETAILS": [{
//	          "Payment_History_Profile": "000000000...",  // 36-char, '0'=ontime, '1'-'6'=DPD, '?'=N/A
//	          "Portfolio_Type": "R",                     // R=revolving, I=installment
//	          "Credit_Limit_Amount": "100000",
//	          "Current_Balance": "45000"
//	        }]
//	      },
//	      "TotalCAPS_Summary": {
//	        "TotalCAPSLast180Days": "5"
//	      }
//	    }
//	  }
//	}
func parseReportInsights(raw json.RawMessage) (*ReportInsights, error) {
	insights := &ReportInsights{}

	// Tolerate the full Digitap envelope being stored in place of the inner
	// result object (a common seed-loading mistake). unwrapResultObject is a
	// no-op when the payload is already the inner object.
	raw = unwrapResultObject(raw)

	// Navigate: result_json -> INProfileResponse
	var wrapper struct {
		ResultJSON struct {
			INProfileResponse struct {
				CAISAccount struct {
					CAISAccountDetails caisAccountDetailList `json:"CAIS_Account_DETAILS"`
				} `json:"CAIS_Account"`
				TotalCAPSSummary struct {
					TotalCAPSLast180Days string `json:"TotalCAPSLast180Days"`
					TotalCAPSLast90Days  string `json:"TotalCAPSLast90Days"`
					TotalCAPSLast30Days  string `json:"TotalCAPSLast30Days"`
					TotalCAPSLast7Days   string `json:"TotalCAPSLast7Days"`
				} `json:"TotalCAPS_Summary"`
				CAPS struct {
					CAPSSummary struct {
						CAPSLast180Days string `json:"CAPSLast180Days"`
						CAPSLast90Days  string `json:"CAPSLast90Days"`
						CAPSLast30Days  string `json:"CAPSLast30Days"`
						CAPSLast7Days   string `json:"CAPSLast7Days"`
					} `json:"CAPS_Summary"`
				} `json:"CAPS"`
			} `json:"INProfileResponse"`
		} `json:"result_json"`
	}
	if err := json.Unmarshal(raw, &wrapper); err != nil {
		return nil, fmt.Errorf("parse credit report response: %w", err)
	}
	profile := wrapper.ResultJSON.INProfileResponse

	// ---- Card utilization, account counts, outstanding, EMI, interest ----
	// Computed in a single pass over all accounts.
	var totalLimit, totalBalance int64
	var activeCount int64
	var totalOutstanding, monthlyEMI, interestPaidPerYear float64
	accountCount := int64(len(profile.CAISAccount.CAISAccountDetails))
	for _, acct := range profile.CAISAccount.CAISAccountDetails {
		balance := atoiSafe64(acct.CurrentBalance)

		// Revolving (credit card) utilization.
		if acct.PortfolioType == "R" {
			limit := atoiSafe64(acct.CreditLimitAmount)
			totalLimit += limit
			totalBalance += balance
		}

		// Active = has a known status that isn't closed ("00") or
		// written-off ("97"). A null/missing status (unmarshalled to "") is
		// treated as unknown, so conservatively NOT counted as active.
		if !isActiveStatus(acct.AccountStatus) {
			continue
		}
		activeCount++
		totalOutstanding += float64(balance)

		// Monthly EMI: Scheduled_Monthly_Payment_Amount for active accounts.
		if emi := atofSafe(acct.ScheduledMonthlyPaymentAmount); emi > 0 {
			monthlyEMI += emi
		}

		// Interest paid per year = outstanding balance * annual rate.
		// Rate_of_Interest is a percentage (e.g. "12.5" = 12.5% p.a.).
		if rate := atofSafe(acct.RateOfInterest); rate > 0 {
			interestPaidPerYear += float64(balance) * rate / 100
		}
	}
	if totalLimit > 0 {
		insights.CardUtilizationPercent = float64(totalBalance) / float64(totalLimit) * 100
		insights.CardUtilizationPercent = float64(int(insights.CardUtilizationPercent*10+0.5)) / 10
	}

	insights.TotalAccountCount = accountCount
	insights.ActiveAccountCount = activeCount
	insights.TotalOutstandingAmount = roundTo2(totalOutstanding)
	insights.MonthlyEMI = roundTo2(monthlyEMI)
	insights.InterestPaidPerYear = roundTo2(interestPaidPerYear)

	// ---- Enquiry counts ----
	caps := profile.TotalCAPSSummary
	insights.EnquiryCount180Days = atoiSafe64(caps.TotalCAPSLast180Days)

	// ---- Loan account list ----
	loanAccounts := make([]LoanAccount, 0, len(profile.CAISAccount.CAISAccountDetails))
	var oldestOpenDate time.Time
	var missedPayments int64
	// On-time months against months the lender actually reported. Accumulated in
	// this loop, from the SAME per-account history the client renders, so the
	// headline percentage and the strip on screen cannot disagree.
	//
	// There used to be three separate hand-rolled readings of the payment field —
	// one here for the list, one for this percentage, one for missedPayments — and
	// they disagreed on every asset-classification character: an all-"S"
	// (Standard, performing) history scored 0% on time AND counted every month as
	// a missed payment.
	var onTime, totalMonths int
	productTypes := map[string]bool{}

	for _, acct := range profile.CAISAccount.CAISAccountDetails {
		originalLoan := atofSafe(acct.HighestCredit)
		balance := atofSafe(acct.CurrentBalance)
		// Computed once and reused for the list, the on-time percentage and the
		// missed-payment count below.
		history := paymentHistoryFor(acct)

		// Percentage paid = (original - current) / original * 100.
		var pctPaid float64
		if originalLoan > 0 {
			pctPaid = (originalLoan - balance) / originalLoan * 100
			if pctPaid < 0 {
				pctPaid = 0
			}
			if pctPaid > 100 {
				pctPaid = 100
			}
			pctPaid = float64(int(pctPaid*10+0.5)) / 10
		}

		totalTenure := atoiSafe64(acct.RepaymentTenure)
		loanAccounts = append(loanAccounts, LoanAccount{
			AccountNumber:         acct.AccountNumber,
			LoanType:              loanTypeFor(acct.AccountType),
			Company:               acct.SubscriberName,
			Active:                isActiveStatus(acct.AccountStatus),
			PercentagePaid:        pctPaid,
			InterestRatePercent:   atofSafe(acct.RateOfInterest),
			TotalTenureMonths:     totalTenure,
			RemainingTenureMonths: remainingTenureMonths(acct.OpenDate, totalTenure),
			CurrentBalance:        roundTo2(balance),
			OriginalLoanAmount:    roundTo2(originalLoan),
			PaymentHistory:        history,
		})

		// Track oldest account open date for credit age.
		if t := parseExperianDate(acct.OpenDate); !t.IsZero() {
			if oldestOpenDate.IsZero() || t.Before(oldestOpenDate) {
				oldestOpenDate = t
			}
		}

		// Missed payments and on-time share, both off the history above.
		for _, m := range history {
			if m.Status == payStatusNotReported {
				continue
			}
			totalMonths++
			if m.Status == payStatusPaid {
				onTime++
			} else {
				missedPayments++
			}
		}

		// Track distinct product types for credit mix.
		if lt := loanTypeFor(acct.AccountType); lt != "Other" {
			productTypes[lt] = true
		}

		// Count serious negatives: written-off ("97") accounts or any account
		// carrying a written-off/settled status flag. A "?"/blank status is not
		// derogatory. This drives the "no defaults/write-offs/settlements"
		// reassurance in the score-builder diagnosis.
		if acct.AccountStatus == "97" || isDerogatoryStatus(acct.WrittenOffSettledStatus) {
			insights.DerogatoryAccounts++
		}
	}
	// Active accounts first, then biggest outstanding balance — the order the
	// accounts screen wants, and the order that answers what a user opening it is
	// actually asking ("what do I still owe, and on what?").
	//
	// Active outranks balance unconditionally, so a fully-repaid-but-open loan
	// still sits above a closed one carrying a settled balance. Closed tradelines
	// are history: they belong below everything live no matter how large they were.
	//
	// Sorted here rather than in each client so every surface that renders this
	// list agrees — the accounts screen, the score reveal's money snapshot, and the
	// loan-switch opportunities derived from it — and so the two clients cannot
	// drift apart.
	//
	// SliceStable, so the bureau's own ordering survives among ties: zero-balance
	// tradelines have no meaningful second key to rank them by.
	sort.SliceStable(loanAccounts, func(i, j int) bool {
		if loanAccounts[i].Active != loanAccounts[j].Active {
			return loanAccounts[i].Active
		}
		return loanAccounts[i].CurrentBalance > loanAccounts[j].CurrentBalance
	})
	// Left nil when nothing was reported: no months means no percentage, and any
	// number here would be invented.
	if totalMonths > 0 {
		pct := float64(onTime) / float64(totalMonths) * 100
		pct = float64(int(pct*10+0.5)) / 10 // 1 decimal place
		insights.OnTimePaymentPercent = &pct
	}

	insights.LoanAccounts = loanAccounts

	// ---- Report card ----
	insights.ReportCard = buildReportCard(reportCardInputs{
		OnTimePercent:    insights.OnTimePaymentPercent,
		MissedPayments:   missedPayments,
		CardUtilization:  insights.CardUtilizationPercent,
		OldestOpenDate:   oldestOpenDate,
		Enquiries180Days: atoiSafe64(caps.TotalCAPSLast180Days),
		Enquiries90Days:  atoiSafe64(caps.TotalCAPSLast90Days),
		Enquiries30Days:  atoiSafe64(caps.TotalCAPSLast30Days),
		Enquiries7Days:   atoiSafe64(caps.TotalCAPSLast7Days),
		ProductTypeCount: len(productTypes),
		ProductTypes:     productTypes,
	})

	return insights, nil
}

// paymentHistoryFor returns one tradeline's month-by-month history, preferring
// CAIS_Account_History over Payment_History_Profile.
//
// Both describe the same thing, but the positional string is the poorer record
// and was the only one being read:
//
//   - It runs a month short. Across every tradeline checked on a real 51-account
//     report, the array carried exactly one more month than the string reported
//     (7 vs 6, 32 vs 31, 25 vs 24, ...). A loan opened in January showed five
//     months of history in August.
//   - Its months can only be dated by counting backwards from "now", which is
//     the time the request is SERVED, not the date the bureau compiled the
//     report. A report pulled weeks ago therefore had every month mislabelled,
//     drifting further the longer ago it was pulled. The array states the year
//     and month outright.
//   - It encodes a DPD bucket, not a number of days. The array gives the exact
//     figure.
//
// The string remains the fallback: some responses (and two stored fixtures)
// carry no history array at all.
func paymentHistoryFor(acct caisAccountDetail) []PaymentMonth {
	if len(acct.CAISAccountHistory) > 0 {
		if history := paymentHistoryFromEntries(acct.CAISAccountHistory); len(history) > 0 {
			return history
		}
	}
	return parsePaymentHistory(acct.PaymentHistoryProfile)
}

// paymentHistoryFromEntries converts CAIS_Account_History into the per-month
// view, newest first (the order the bureau already sends).
//
// Presence in the array means the month was reported, so there is no
// "not reported" case to synthesize — unreported months are simply absent.
func paymentHistoryFromEntries(entries []caisAccountHistoryEntry) []PaymentMonth {
	out := make([]PaymentMonth, 0, len(entries))
	for _, e := range entries {
		label := monthLabelFrom(e.Year, e.Month)
		if label == "" {
			continue // undateable row: nothing useful to show against it
		}
		pm := PaymentMonth{Month: label}
		if dpd, ok := parseDaysPastDue(e.DaysPastDue); ok {
			pm.DaysLate = dpd
			if dpd > 0 {
				pm.Status = payStatusDelayed
			} else {
				pm.Status = payStatusPaid
			}
		} else if ac := strings.TrimSpace(e.AssetClassification); ac != "" {
			// No usable DPD — fall back to the classification's leading
			// character, which is what the profile string would have carried
			// ("STD" and "S" both mean Standard).
			pm.Status, pm.DaysLate = classifyPaymentChar(ac[0])
		} else {
			pm.Status = payStatusNotReported
		}
		out = append(out, pm)
	}
	return out
}

// monthLabelFrom formats a "YYYY-MM" label from the bureau's string year and
// month. Empty when either is missing or out of range, so a malformed row is
// dropped rather than dated to year zero.
func monthLabelFrom(year, month string) string {
	y, err := strconv.Atoi(strings.TrimSpace(year))
	if err != nil || y < 1900 || y > 2999 {
		return ""
	}
	m, err := strconv.Atoi(strings.TrimSpace(month))
	if err != nil || m < 1 || m > 12 {
		return ""
	}
	return fmt.Sprintf("%04d-%02d", y, m)
}

// parseDaysPastDue parses the zero-padded Days_Past_Due string ("000", "045").
// Reports false for an empty or non-numeric value — including the "?" and "XXX"
// placeholders — so the caller can fall back rather than reading them as zero
// days late, which would count a delinquent month as paid on time.
func parseDaysPastDue(raw string) (int, bool) {
	v := strings.TrimSpace(raw)
	if v == "" {
		return 0, false
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}

// parsePaymentHistory decodes the 36-character Payment_History_Profile string
// into per-month entries. The bureau convention: position 0 = most recent
// month, each position going back one month.
//
// Character semantics live in [classifyPaymentChar], transcribed from the
// "Asset Classification Code" table in section 1.9 of the spec V2.7. In short:
// '0'-'6' are 30-day DPD buckets ('0' = 0-29 ... '6' = 180 or more), 'S'/'B'/'D'/'M'
// are asset classifications, and 'N'/'?' mean the month was not reported.
//
// Month labels are assigned relative to the report generation date (position
// 0 = the report month). The most recent month is returned first.
func parsePaymentHistory(php string) []PaymentMonth {
	if php == "" {
		return []PaymentMonth{}
	}

	now := time.Now().UTC()
	history := make([]PaymentMonth, 0, len(php))

	for i := 0; i < len(php); i++ {
		// Position 0 = current month; position i = i months ago.
		monthLabel := now.AddDate(0, -i, 0).Format("2006-01")
		status, daysLate := classifyPaymentChar(php[i])
		history = append(history, PaymentMonth{
			Month:    monthLabel,
			Status:   status,
			DaysLate: daysLate,
		})
	}

	return history
}

// Values of [PaymentMonth.Status]. Named because the on-time percentage branches
// on them too, and a typo in either place would silently skew the figure.
const (
	payStatusPaid        = "paid"
	payStatusDelayed     = "delayed"
	payStatusNotReported = "not_reported"
)

// paymentRatings maps one Payment_History_Profile character to a status and the
// LOWER BOUND of its days-past-due bucket, per the "Asset Classification Code"
// table in section 1.9 of the spec V2.7.
//
// The buckets are 30 days wide from '1' up ('1' = 30-59, '2' = 60-89, ...
// '6' = 180 or more). An earlier 1/31/61/91/121/151 was a bucket short at every
// level, so a 60-day delinquency was reported as 31 days late.
var paymentRatings = map[byte]struct {
	status   string
	daysLate int
}{
	// '0' is 0-29 days: the bureau's own on-time bucket, not necessarily
	// "paid on the due date".
	'0': {payStatusPaid, 0},
	'1': {payStatusDelayed, 30},
	'2': {payStatusDelayed, 60},
	'3': {payStatusDelayed, 90},
	'4': {payStatusDelayed, 120},
	'5': {payStatusDelayed, 150},
	'6': {payStatusDelayed, 180},

	// Asset classifications. 'S' (Standard) means the account is performing, and
	// it appears in real reports — whole histories of it. It used to fall through
	// to "not reported", which zeroed the on-time percentage for accounts that
	// had in fact never missed a payment.
	'S': {payStatusPaid, 0},
	// Substandard / Doubtful / Special Mention are adverse but carry no DPD
	// figure of their own. The bounds below are the RBI definitions of those
	// classifications (NPA at 90+, SMA as pre-NPA stress), NOT numbers the bureau
	// supplied — revisit if the UI ever presents the figure as exact.
	'B': {payStatusDelayed, 90},
	'D': {payStatusDelayed, 90},
	'M': {payStatusDelayed, 30},
}

// classifyPaymentChar is the single source of truth for one
// Payment_History_Profile character: the per-month history the client renders
// and the headline on-time percentage both go through it, so they cannot
// disagree about what a character means.
func classifyPaymentChar(ch byte) (status string, daysLate int) {
	// The spec pairs 'N' with '?' as "value not available"; both occur in real
	// reports. An unrecognized character is treated the same way — conservatively
	// unreported rather than counted as either on-time or late.
	switch ch {
	case '?', ' ', 'N', 'n':
		return payStatusNotReported, 0
	}
	if info, ok := paymentRatings[ch]; ok {
		return info.status, info.daysLate
	}
	return payStatusNotReported, 0
}

// ---- Report card grading ---------------------------------------------------

// reportCardInputs is the computed data the grading functions consume.
type reportCardInputs struct {
	OnTimePercent    *float64
	MissedPayments   int64
	CardUtilization  float64
	OldestOpenDate   time.Time // zero value = no open date found
	Enquiries180Days int64
	Enquiries90Days  int64
	Enquiries30Days  int64
	Enquiries7Days   int64
	ProductTypeCount int
	ProductTypes     map[string]bool
}

// buildReportCard grades each of the five credit factors and derives an
// overall grade. The weights follow the FICO model: payment history 35%,
// credit utilization 30%, credit age 15%, enquiries 10%, credit mix 10%.
func buildReportCard(in reportCardInputs) *ReportCard {
	card := &ReportCard{}

	// 1. Payment history (35%)
	//
	// Omitted entirely when it cannot be graded. An ungraded factor row would be
	// worse than absent: overallGrade scores an unrecognised grade as zero, so a
	// placeholder would drag the overall down exactly as an F does, and the client
	// would render a factor with a blank badge.
	if phGrade, phSum, phDetail := gradePaymentHistory(in.OnTimePercent, in.MissedPayments); phGrade != "" {
		card.Factors = append(card.Factors, CardFactor{
			Name: "Payment history", Weight: 35, Grade: phGrade,
			Summary: phSum, Detail: phDetail, MissedCount: in.MissedPayments,
		})
	}

	// 2. Credit utilisation (30%)
	cuGrade, cuSum, cuDetail := gradeUtilization(in.CardUtilization)
	card.Factors = append(card.Factors, CardFactor{
		Name: "Credit utilisation", Weight: 30, Grade: cuGrade,
		Summary: cuSum, Detail: cuDetail,
	})

	// 3. Credit age (15%)
	caGrade, caSum, caDetail := gradeCreditAge(in.OldestOpenDate)
	card.Factors = append(card.Factors, CardFactor{
		Name: "Credit age", Weight: 15, Grade: caGrade,
		Summary: caSum, Detail: caDetail,
	})

	// 4. Enquiries (10%)
	enqGrade, enqSum, enqDetail := gradeEnquiries(in.Enquiries180Days)
	card.Factors = append(card.Factors, CardFactor{
		Name: "Enquiries", Weight: 10, Grade: enqGrade,
		Summary: enqSum, Detail: enqDetail,
	})

	// 5. Credit mix (10%)
	cmGrade, cmSum, cmDetail := gradeCreditMix(in.ProductTypeCount, in.ProductTypes)
	card.Factors = append(card.Factors, CardFactor{
		Name: "Credit mix", Weight: 10, Grade: cmGrade,
		Summary: cmSum, Detail: cmDetail,
	})

	card.OverallGrade = overallGrade(card.Factors)
	return card
}

// gradePaymentHistory grades based on on-time percentage and missed count.
// A nil onTimePct means no month was reported. It returns an EMPTY grade, which
// buildReportCard turns into an omitted factor rather than a bad one: grading a
// file with no payment data produced "F — Critical. Immediate action required.",
// which is a serious thing to tell someone on the strength of nothing.
func gradePaymentHistory(onTimePct *float64, missed int64) (grade, summary, detail string) {
	if onTimePct == nil {
		return "", "No payment history reported yet by your lenders.",
			"This fills in as your lenders report their first months."
	}
	pct := *onTimePct
	switch {
	case pct >= 99 && missed == 0:
		return "A+", "No missed payments. Excellent track record.", "Keep the streak alive."
	case pct >= 95:
		return "A", fmt.Sprintf("%d missed/delayed payment(s). Strong history.", missed), "Set auto-pay to eliminate lapses."
	case pct >= 85:
		return "B", fmt.Sprintf("%d missed/delayed payment(s). Room to improve.", missed), "Prioritize on-time payments for 6 months."
	case pct >= 70:
		return "C", fmt.Sprintf("%d missed/delayed payment(s). Needs attention.", missed), "No more missed payments for 12 months."
	case pct >= 50:
		return "D", fmt.Sprintf("%d missed/delayed payment(s). High risk signal.", missed), "Restructure debts; seek counseling."
	default:
		return "F", fmt.Sprintf("%d missed/delayed payment(s). Critical.", missed), "Immediate action required."
	}
}

// gradeUtilization grades based on revolving credit utilization percentage.
func gradeUtilization(pct float64) (grade, summary, detail string) {
	switch {
	case pct == 0:
		return "A+", "No revolving balances. Optimal.", "Maintain zero utilization."
	case pct < 10:
		return "A+", fmt.Sprintf("%.1f%% utilization. Elite.", pct), "Stay under 10% for top-tier scores."
	case pct < 30:
		return "A", fmt.Sprintf("%.1f%% utilization. Under 30%% — good.", pct), "Push under 10% for A+."
	case pct < 50:
		return "B", fmt.Sprintf("%.1f%% utilization. Manageable but high.", pct), "Pay down to under 30%."
	case pct < 75:
		return "C", fmt.Sprintf("%.1f%% utilization. High risk.", pct), "Aggressively pay down balances."
	default:
		return "D", fmt.Sprintf("%.1f%% utilization. Maxed out.", pct), "Stop new charges; focus on repayment."
	}
}

// gradeCreditAge grades based on the age of the oldest account in years.
func gradeCreditAge(oldest time.Time) (grade, summary, detail string) {
	if oldest.IsZero() {
		return "B", "Credit age unavailable.", "Build history over time."
	}
	years := time.Since(oldest).Hours() / 24 / 365
	switch {
	case years >= 10:
		return "A+", fmt.Sprintf("%.1f years — long-established history.", years), "Protect old accounts; they anchor your score."
	case years >= 5:
		return "A", fmt.Sprintf("%.1f years of credit history.", years), "Keep your oldest card open."
	case years >= 3:
		return "B", fmt.Sprintf("%.1f years — still maturing.", years), "Avoid closing old accounts."
	case years >= 1:
		return "C", fmt.Sprintf("%.1f years — young profile.", years), "Time is your ally; keep accounts open."
	default:
		return "D", "Less than 1 year — thin file.", "Build gradually with responsible use."
	}
}

// gradeEnquiries grades based on hard enquiry count in the past 180 days.
func gradeEnquiries(count180 int64) (grade, summary, detail string) {
	switch {
	case count180 == 0:
		return "A+", "0 enquiries in 6 months. Lenders see zero credit hunger.", "Maintain discipline."
	case count180 <= 2:
		return "A", fmt.Sprintf("%d enquiries in 6 months. Normal.", count180), "Space out new applications."
	case count180 <= 4:
		return "B", fmt.Sprintf("%d enquiries in 6 months. Moderate.", count180), "Pause new applications for 6 months."
	case count180 <= 6:
		return "C", fmt.Sprintf("%d enquiries in 6 months. Elevated.", count180), "Stop applying; let enquiries age off."
	default:
		return "D", fmt.Sprintf("%d enquiries in 6 months. High risk signal.", count180), "No new applications for 12 months."
	}
}

// gradeCreditMix grades based on the count of distinct product types held.
func gradeCreditMix(count int, types map[string]bool) (grade, summary, detail string) {
	// Build a sorted product list for the summary line.
	products := make([]string, 0, len(types))
	for t := range types {
		products = append(products, strings.ToLower(t))
	}
	sort.Strings(products)
	productList := strings.Join(products, ", ")

	switch {
	case count >= 4:
		return "A+", fmt.Sprintf("%d product types: %s.", count, productList), "Diverse mix — well-managed portfolio."
	case count >= 3:
		return "A", fmt.Sprintf("%d product types: %s.", count, productList), "Good mix of revolving and installment."
	case count == 2:
		return "B", fmt.Sprintf("%d product types: %s.", count, productList), "Add another type to diversify."
	case count == 1:
		return "C", fmt.Sprintf("Only 1 product type: %s.", productList), "Add a different credit type to strengthen mix."
	default:
		return "D", "No active credit products.", "Start with a secured card or small loan."
	}
}

// overallGrade computes the weighted average of factor grades and returns
// the overall letter grade. Each grade maps to a numeric score: A+=5, A=4,
// B=3, C=2, D=1, F=0.
func overallGrade(factors []CardFactor) string {
	gradeScore := map[string]float64{"A+": 5, "A": 4, "B": 3, "C": 2, "D": 1, "F": 0}
	var totalWeight, weightedSum float64
	for _, f := range factors {
		totalWeight += float64(f.Weight)
		weightedSum += gradeScore[f.Grade] * float64(f.Weight)
	}
	if totalWeight == 0 {
		return "B"
	}
	avg := weightedSum / totalWeight
	switch {
	case avg >= 4.5:
		return "A+"
	case avg >= 3.5:
		return "A"
	case avg >= 2.5:
		return "B"
	case avg >= 1.5:
		return "C"
	case avg >= 0.5:
		return "D"
	default:
		return "F"
	}
}

// remainingTenureMonths estimates how many months are left on an installment
// loan: its total tenure minus the months elapsed since the open date, clamped
// to [0, total]. Returns 0 when the tenure is unknown (0) or the open date is
// unparseable — callers must treat 0 as "unknown", the same convention the
// bureau's sparse fields force elsewhere.
func remainingTenureMonths(openDate string, totalTenure int64) int64 {
	if totalTenure <= 0 {
		return 0
	}
	opened := parseExperianDate(openDate)
	if opened.IsZero() {
		return 0
	}
	now := time.Now().UTC()
	elapsed := int64((now.Year()-opened.Year())*12 + int(now.Month()) - int(opened.Month()))
	if elapsed < 0 {
		elapsed = 0
	}
	remaining := totalTenure - elapsed
	if remaining < 0 {
		return 0
	}
	if remaining > totalTenure {
		return totalTenure
	}
	return remaining
}

// parseExperianDate parses an Experian date string in YYYYMMDD format
// (e.g. "20150312"). Returns the zero time if the string is empty/invalid.
func parseExperianDate(s string) time.Time {
	if len(s) < 8 {
		return time.Time{}
	}
	t, err := time.Parse("20060102", s[:8])
	if err != nil {
		return time.Time{}
	}
	return t
}

// atofSafe parses a numeric prefix of s as float64. Non-numeric or empty
// strings return 0. Handles decimal values (e.g. "12.5").
func atofSafe(s string) float64 {
	var intPart, fracPart float64
	var divisor float64 = 1
	inFrac := false
	for _, c := range s {
		switch {
		case c >= '0' && c <= '9':
			if inFrac {
				divisor *= 10
				fracPart = fracPart*10 + float64(c-'0')
			} else {
				intPart = intPart*10 + float64(c-'0')
			}
		case c == '.' && !inFrac:
			inFrac = true
		default:
			return (intPart + fracPart/divisor)
		}
	}
	return intPart + fracPart/divisor
}

// roundTo2 rounds to 2 decimal places.
func roundTo2(v float64) float64 {
	return float64(int(v*100+0.5)) / 100
}

// isActiveStatus reports whether the Experian Account_Status code represents
// an active, open tradeline. Closed ("00") and written-off ("97") are inactive.
// An empty/missing/null status (unmarshalled to "") is treated as unknown and
// therefore NOT active, so a null upstream status can't inflate the active
// count or outstanding balance.
// closedAccountStatuses are the Account_Status values the spec's account-status
// master (section 1.9) maps to CLOSED. Everything else is ACTIVE, including
// values absent from the table — the master says so explicitly with its
// "DEFAULTVALUE ACTIVE" row.
//
// The three in the second group carry no ACTIVE/CLOSED tag of their own; they
// are listed among the later 130-138 status descriptions, and each description
// says closed outright. They are treated as closed on the strength of that,
// because the alternative is counting a written-off, closed account toward the
// user's live balances.
var closedAccountStatuses = map[string]bool{
	"12": true, "13": true, "14": true, "15": true, "16": true, "17": true,

	"132": true, // Post Write Off Closed
	"133": true, // Restructured & Closed
	"138": true, // Entity ceased while account was closed
}

// isActiveStatus reports whether a CAIS Account_Status means the tradeline is
// still open.
//
// Only the CLOSED codes above close an account. The previous rule — everything
// except "", "00" and "97" is active — was wrong in both directions against the
// master table, and expensively so:
//
//   - "13" is CLOSED and is the most common status in real reports, so every
//     closed account was counted in the active total and its balance added to
//     the user's outstanding debt.
//   - "00" is "No Suit Filed", the ordinary state of a healthy live account, and
//     was being hidden from the active count.
//   - "97" is "Suit Filed (Wilful Default) and Written-off": derogatory, which
//     is counted separately, but not closed — the liability is still open.
func isActiveStatus(status string) bool {
	code := normalizeCode(status)
	// An absent status is not the master's "DEFAULTVALUE ACTIVE" case: that row
	// covers a value the table does not list, not a field the bureau never sent.
	// Digitap leaves it null on some revolving cards, and treating no evidence as
	// an open account would add its balance to the user's live debt.
	if code == "" {
		return false
	}
	return !closedAccountStatuses[code]
}

// atoiSafe64 parses a numeric prefix of s as int64. Non-numeric or empty
// strings return 0.
func atoiSafe64(s string) int64 {
	n := int64(0)
	for _, c := range s {
		if c < '0' || c > '9' {
			break
		}
		n = n*10 + int64(c-'0')
	}
	return n
}

// generateClientRefNum returns "CA-<unixmilli>-<6 hex chars>", unique per call
// via the combination of wall clock + crypto random tail.
func generateClientRefNum() string {
	return fmt.Sprintf("%s-%d-%s", clientRefPrefix, time.Now().UnixMilli(), randomHex(3))
}

// randomHex returns n random bytes as a 2*n-char lowercase hex string.
func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand.Read only errors on a broken reader, which is fatal; fall
		// back to a constant so we never fail the request over a correlation id.
		return "000000"
	}
	return hex.EncodeToString(b)
}

// generateOTP returns a zero-padded n-digit numeric string using a
// crypto-backed uniform draw.
func generateOTP(n int) (string, error) {
	max := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(n)), nil)
	v, err := rand.Int(rand.Reader, max)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%0*d", n, v), nil
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// keysOf returns the map's keys as a slice, for logging field names without
// their (potentially PII-bearing) values.
func keysOf(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
