package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"net/http"
	"sort"
	"strings"
	"time"

	"credit-report-service/internal/apperr"
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
	reportTypeFlag = 0
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
}

func NewCreditAnalyticsService(
	client *digitap.Client,
	repo *repository.CreditAnalyticsRepo,
	accounts *repository.AccountRepo,
) *CreditAnalyticsService {
	return &CreditAnalyticsService{client: client, repo: repo, accounts: accounts}
}

// CreditAnalyticsInput is the validated payload for a credit-analytics request.
// With the payload now built server-side, device_ip is the only field the
// caller contributes.
type CreditAnalyticsInput struct {
	DeviceIP string `json:"device_ip"`
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

// ReportInsights is the derived analytics from the latest successful credit
// report: the bureau credit score, on-time payment percentage, card
// utilization percentage, and enquiry count for the past 180 days.
type ReportInsights struct {
	ReportID               int64         `json:"reportId"`
	CreditScore            *int64        `json:"creditScore"`
	OnTimePaymentPercent   float64       `json:"onTimePaymentPercent"`
	CardUtilizationPercent float64       `json:"cardUtilizationPercent"`
	EnquiryCount180Days    int64         `json:"enquiryCount180Days"`
	Outdated               bool          `json:"outdated"`
	TotalAccountCount      int64         `json:"totalAccountCount"`
	ActiveAccountCount     int64         `json:"activeAccountCount"`
	TotalOutstandingAmount float64       `json:"totalOutstandingAmount"`
	MonthlyEMI             float64       `json:"monthlyEmi"`
	InterestPaidPerYear    float64       `json:"interestPaidPerYear"`
	LoanAccounts           []LoanAccount `json:"loanAccounts"`
	ReportCard             *ReportCard   `json:"reportCard"`
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
type LoanAccount struct {
	AccountNumber      string         `json:"accountNumber"`
	LoanType           string         `json:"loanType"`
	Company            string         `json:"company"`
	PercentagePaid     float64        `json:"percentagePaid"`
	TotalTenureMonths  int64          `json:"totalTenureMonths"`
	CurrentBalance     float64        `json:"currentBalance"`
	OriginalLoanAmount float64        `json:"originalLoanAmount"`
	PaymentHistory     []PaymentMonth `json:"paymentHistory"`
}

// PaymentMonth is one month's payment status in the 36-month history.
type PaymentMonth struct {
	Month    string `json:"month"`    // e.g. "2026-08"
	Status   string `json:"status"`   // "paid", "delayed", "not_reported"
	DaysLate int    `json:"daysLate"` // 0 if paid on time
}

// accountTypeMap translates Experian Account_Type codes to human-readable
// loan type names. Based on the Digitap V2.7 spec.
var accountTypeMap = map[string]string{
	"01": "Auto Loan",
	"02": "Auto Loan",
	"03": "Auto Loan",
	"04": "Two Wheeler Loan",
	"05": "Two Wheeler Loan",
	"06": "Personal Loan",
	"07": "Home Loan",
	"08": "Property Loan",
	"09": "Credit Card",
	"10": "Credit Card",
	"11": "Consumer Loan",
	"12": "Education Loan",
	"13": "Overdraft",
	"14": "Business Loan",
	"15": "Business Loan",
	"25": "Commercial Vehicle Loan",
	"26": "Tractor Loan",
	"27": "Gold Loan",
	"28": "Loan Against Shares",
	"29": "Loan Against FD",
	"30": "Corporate Credit Card",
	"31": "Leasing",
	"32": "Consumer Durable",
	"33": "Consumer Durable",
	"34": "Used Car Loan",
	"35": "Loan Against Debentures",
	"36": "Loan Against Mutual Funds",
	"37": "Construction Equipment Loan",
	"38": "Used Two Wheeler Loan",
	"39": "Used Three Wheeler Loan",
	"40": "Loan Against jewellery",
	"41": "Commercial Real Estate Loan",
	"42": "Heavy Commercial Vehicle Loan",
	"43": "Medium Commercial Vehicle Loan",
	"44": "Light Commercial Vehicle Loan",
	"45": "Loan Against Car",
	"46": "Kisan Card",
	"47": "Doctor Loan",
	"48": "Engineer Loan",
	"49": "CA Loan",
	"50": "Loan Against Property",
	"51": "Personal Computer Loan",
	"52": "Mobile Phone Loan",
	"53": "Scooter Loan",
	"54": "Truck Loan",
	"55": "Housing Loan",
	"56": "Staff Loan",
	"57": "Staff Loan",
	"58": "Bank Loan",
	"59": "Loan Against Savings Certificates",
	"60": "Secured Credit Card",
	"61": "Transaction Loan",
	"62": "Agricultural Loan",
	"63": "Group Agricultural Loan",
	"64": "Agri Allied Activities Loan",
	"65": "Mortgage Loan",
	"66": "Microfinance Loan",
	"67": "Pradhan Mantri Awas Yojana",
	"68": "Small Business Loan",
	"69": "Working Capital Loan",
	"70": "Term Loan",
}

// loanTypeFor returns the human-readable loan type for an Experian
// Account_Type code, falling back to "Other" if unknown.
func loanTypeFor(accountType string) string {
	if name, ok := accountTypeMap[accountType]; ok {
		return name
	}
	return "Other"
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
	return d
}

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
// Upstream HTTP statuses are mapped to typed app errors:
//   - 200: success (result_code 101/102/103 all returned as a persisted row)
//   - 400: validation -> apperr.Validation
//   - 401: auth        -> apperr.Unauthorized
//   - 422: tradeline   -> apperr.PanFailure
//   - 5xx / other:     -> apperr (surfaced as 400 by the handler)
//
// A row is persisted before any error is returned, so failed upstream calls are
// still queryable in the DB.
func (s *CreditAnalyticsService) Request(ctx context.Context, accountID int64, in CreditAnalyticsInput) (*models.CreditAnalyticsRequest, error) {
	if details := in.validate(); len(details) > 0 {
		return nil, apperr.NewValidationWith("invalid credit-analytics request", details)
	}

	payload, err := s.buildPayload(ctx, accountID, in.DeviceIP)
	if err != nil {
		return nil, err
	}

	reqBody, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal credit-analytics request: %w", err)
	}

	// Time the upstream call so latency is observable independently of the
	// request-scoped middleware timing.
	upstart := time.Now()
	env, httpStatus, err := s.client.Request(ctx, payload)
	upstreamLatency := time.Since(upstart).Milliseconds()
	if err != nil {
		// Network/transport failure — persist what we have, then surface the error.
		row := s.buildRow(accountID, payload, reqBody)
		_ = s.persist(ctx, row)
		slog.Error("credit-analytics upstream unreachable",
			"account_id", accountID,
			"client_ref_num", payload.ClientRefNum,
			"latency_ms", upstreamLatency,
			"error", err,
		)
		return nil, apperr.NewValidation("credit-analytics provider unreachable: " + err.Error())
	}

	row := s.buildRow(accountID, payload, reqBody)
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
		return nil, persistErr
	}

	// Map the upstream status to a typed error. The row is already persisted.
	switch {
	case httpStatus >= 200 && httpStatus < 300:
		slog.Info("credit-analytics request succeeded",
			"account_id", accountID,
			"report_id", row.ID,
			"client_ref_num", payload.ClientRefNum,
			"upstream_status", httpStatus,
			"result_code", env.ResultCode,
			"latency_ms", upstreamLatency,
		)
		return row, nil
	case httpStatus == http.StatusBadRequest:
		slog.Warn("credit-analytics upstream bad request",
			"account_id", accountID,
			"report_id", row.ID,
			"upstream_status", httpStatus,
			"message", env.Message,
		)
		return row, apperr.NewValidation(env.Message)
	case httpStatus == http.StatusUnauthorized:
		slog.Warn("credit-analytics upstream unauthorized",
			"account_id", accountID,
			"report_id", row.ID,
			"upstream_status", httpStatus,
		)
		return row, apperr.NewUnauthorized(env.Message)
	case httpStatus == http.StatusUnprocessableEntity:
		slog.Warn("credit-analytics upstream tradeline rejection",
			"account_id", accountID,
			"report_id", row.ID,
			"upstream_status", httpStatus,
			"message", env.Message,
		)
		return row, apperr.NewPanFailure(env.Message)
	default:
		slog.Warn("credit-analytics upstream error",
			"account_id", accountID,
			"report_id", row.ID,
			"upstream_status", httpStatus,
			"message", env.Message,
		)
		return row, apperr.NewValidation(fmt.Sprintf("digitap upstream error (%d): %s", httpStatus, env.Message))
	}
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
func (s *CreditAnalyticsService) buildRow(accountID int64, p *digitapPayload, reqBody []byte) *models.CreditAnalyticsRequest {
	return &models.CreditAnalyticsRequest{
		AccountID:    &accountID,
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
	return s.insightsFromRow(row)
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
	return s.insightsFromRow(row)
}

// insightsFromRow derives the analytics view of a stored report row. The bureau
// payload is parsed when present; a row without a stored response (a failed
// pull / no-record result) still yields the metadata fields (report id, score,
// outdated) with zeroed analytics rather than an error. The score prefers the
// persisted column and falls back to parsing the response so reports created
// before the column existed still report a score.
func (s *CreditAnalyticsService) insightsFromRow(row *models.CreditAnalyticsRequest) (*ReportInsights, error) {
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
	insights.Outdated = time.Since(row.CreatedAt) > 30*24*time.Hour
	return insights, nil
}

// extractBureauScore lifts SCORE.BureauScore out of the raw Digitap
// INProfileResponse envelope for cheap per-row storage. It returns nil when the
// payload is empty, unparseable, or carries no numeric score (e.g. a failed
// pull or a no-record response), so a missing score is never stored as 0.
func extractBureauScore(raw json.RawMessage) *int64 {
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

	// Navigate: result_json -> INProfileResponse
	var wrapper struct {
		ResultJSON struct {
			INProfileResponse struct {
				CAISAccount struct {
					CAISAccountDetails []struct {
						PaymentHistoryProfile         string `json:"Payment_History_Profile"`
						PortfolioType                 string `json:"Portfolio_Type"`
						CreditLimitAmount             string `json:"Credit_Limit_Amount"`
						CurrentBalance                string `json:"Current_Balance"`
						AccountStatus                 string `json:"Account_Status"`
						ScheduledMonthlyPaymentAmount string `json:"Scheduled_Monthly_Payment_Amount"`
						RateOfInterest                string `json:"Rate_of_Interest"`
						AccountType                   string `json:"Account_Type"`
						AccountNumber                 string `json:"Account_Number"`
						SubscriberName                string `json:"Subscriber_Name"`
						OpenDate                      string `json:"Open_Date"`
						HighestCredit                 string `json:"Highest_Credit_or_Original_Loan_Amount"`
						RepaymentTenure               string `json:"Repayment_Tenure"`
					} `json:"CAIS_Account_DETAILS"`
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

	// ---- On-time payment percentage ----
	// Across all accounts, count months where payment was on-time ('0') vs
	// total reported months (anything except '?').
	var onTime, totalMonths int
	for _, acct := range profile.CAISAccount.CAISAccountDetails {
		for _, ch := range acct.PaymentHistoryProfile {
			if ch == '?' {
				continue
			}
			totalMonths++
			if ch == '0' {
				onTime++
			}
		}
	}
	if totalMonths > 0 {
		insights.OnTimePaymentPercent = float64(onTime) / float64(totalMonths) * 100
		// Round to 1 decimal place.
		insights.OnTimePaymentPercent = float64(int(insights.OnTimePaymentPercent*10+0.5)) / 10
	}

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
	productTypes := map[string]bool{}

	for _, acct := range profile.CAISAccount.CAISAccountDetails {
		originalLoan := atofSafe(acct.HighestCredit)
		balance := atofSafe(acct.CurrentBalance)

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

		loanAccounts = append(loanAccounts, LoanAccount{
			AccountNumber:      acct.AccountNumber,
			LoanType:           loanTypeFor(acct.AccountType),
			Company:            acct.SubscriberName,
			PercentagePaid:     pctPaid,
			TotalTenureMonths:  atoiSafe64(acct.RepaymentTenure),
			CurrentBalance:     roundTo2(balance),
			OriginalLoanAmount: roundTo2(originalLoan),
			PaymentHistory:     parsePaymentHistory(acct.PaymentHistoryProfile),
		})

		// Track oldest account open date for credit age.
		if t := parseExperianDate(acct.OpenDate); !t.IsZero() {
			if oldestOpenDate.IsZero() || t.Before(oldestOpenDate) {
				oldestOpenDate = t
			}
		}

		// Count missed/delayed payments across all accounts.
		for _, ch := range acct.PaymentHistoryProfile {
			if ch != '?' && ch != ' ' && ch != '0' {
				missedPayments++
			}
		}

		// Track distinct product types for credit mix.
		if lt := loanTypeFor(acct.AccountType); lt != "Other" {
			productTypes[lt] = true
		}
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

// parsePaymentHistory decodes the 36-character Payment_History_Profile string
// into per-month entries. The bureau convention: position 0 = most recent
// month, each position going back one month.
//
// Payment rating codes:
//
//	'0' = current / paid on time
//	'1' = 1-30 days past due
//	'2' = 31-60 days past due
//	'3' = 61-90 days past due
//	'4' = 91-120 days past due
//	'5' = 121-150 days past due
//	'6' = 151+ days past due
//	'?' = not reported (before account opened)
//
// Month labels are assigned relative to the report generation date (position
// 0 = the report month). The most recent month is returned first.
func parsePaymentHistory(php string) []PaymentMonth {
	if php == "" {
		return []PaymentMonth{}
	}

	// Rating -> (status, approx days late). Days late are the lower bound of
	// the DPD bucket for that rating.
	ratingMap := map[byte]struct {
		status   string
		daysLate int
	}{
		'0': {"paid", 0},
		'1': {"delayed", 1},
		'2': {"delayed", 31},
		'3': {"delayed", 61},
		'4': {"delayed", 91},
		'5': {"delayed", 121},
		'6': {"delayed", 151},
	}

	now := time.Now().UTC()
	history := make([]PaymentMonth, 0, len(php))

	for i := 0; i < len(php); i++ {
		ch := php[i]
		// Position 0 = current month; position i = i months ago.
		monthDate := now.AddDate(0, -i, 0)
		monthLabel := monthDate.Format("2006-01")

		pm := PaymentMonth{Month: monthLabel}
		if ch == '?' || ch == ' ' {
			pm.Status = "not_reported"
		} else if info, ok := ratingMap[ch]; ok {
			pm.Status = info.status
			pm.DaysLate = info.daysLate
		} else {
			// Unknown code — treat conservatively as not reported.
			pm.Status = "not_reported"
		}
		history = append(history, pm)
	}

	return history
}

// ---- Report card grading ---------------------------------------------------

// reportCardInputs is the computed data the grading functions consume.
type reportCardInputs struct {
	OnTimePercent    float64
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
	phGrade, phSum, phDetail := gradePaymentHistory(in.OnTimePercent, in.MissedPayments)
	card.Factors = append(card.Factors, CardFactor{
		Name: "Payment history", Weight: 35, Grade: phGrade,
		Summary: phSum, Detail: phDetail, MissedCount: in.MissedPayments,
	})

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
func gradePaymentHistory(onTimePct float64, missed int64) (grade, summary, detail string) {
	switch {
	case onTimePct >= 99 && missed == 0:
		return "A+", "No missed payments. Excellent track record.", "Keep the streak alive."
	case onTimePct >= 95:
		return "A", fmt.Sprintf("%d missed/delayed payment(s). Strong history.", missed), "Set auto-pay to eliminate lapses."
	case onTimePct >= 85:
		return "B", fmt.Sprintf("%d missed/delayed payment(s). Room to improve.", missed), "Prioritize on-time payments for 6 months."
	case onTimePct >= 70:
		return "C", fmt.Sprintf("%d missed/delayed payment(s). Needs attention.", missed), "No more missed payments for 12 months."
	case onTimePct >= 50:
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
func isActiveStatus(status string) bool {
	switch status {
	case "", "00", "97":
		return false
	default:
		return true
	}
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
