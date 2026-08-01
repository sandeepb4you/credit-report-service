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

// ReportSummary is the trimmed list-item shape for the reports endpoint: just
// the report's unique identifier (id) and generation date (created_at).
type ReportSummary struct {
	ID        int64     `json:"id"`
	CreatedAt time.Time `json:"createdAt"`
}

// ReportPage is the paginated reports-list response.
type ReportPage struct {
	Items []ReportSummary `json:"items"`
	Page  int             `json:"page"`
	Size  int             `json:"size"`
	Total int64           `json:"total"`
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
		items = append(items, ReportSummary{ID: r.ID, CreatedAt: r.CreatedAt})
	}
	return &ReportPage{Items: items, Page: page, Size: size, Total: total}, nil
}

// GetReport returns the full report row for the caller's own report. A row
// belonging to another account is reported as not found (no existence leak).
func (s *CreditAnalyticsService) GetReport(ctx context.Context, accountID, id int64) (*models.CreditAnalyticsRequest, error) {
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
