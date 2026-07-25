package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"credit-report-service/internal/apperr"
	"credit-report-service/internal/digitap"
	"credit-report-service/internal/models"
	"credit-report-service/internal/repository"
)

// CreditAnalyticsService proxies a credit-analysis request to the Digitap
// /credit_analytics/request API and persists both the request and the upstream
// response to the credit_analytics_requests table.
type CreditAnalyticsService struct {
	client *digitap.Client
	repo   *repository.CreditAnalyticsRepo
}

func NewCreditAnalyticsService(client *digitap.Client, repo *repository.CreditAnalyticsRepo) *CreditAnalyticsService {
	return &CreditAnalyticsService{client: client, repo: repo}
}

// CreditAnalyticsInput is the validated payload for a credit-analytics request.
// It mirrors the request body documented in section 1.4.1 of the Digitap spec.
// Pointer fields distinguish "omitted" from "set to zero".
type CreditAnalyticsInput struct {
	ClientRefNum      string  `json:"client_ref_num"`
	MobileNo          string  `json:"mobile_no"`
	NameLookup        *int    `json:"name_lookup,omitempty"`
	FirstName         *string `json:"first_name,omitempty"`
	LastName          *string `json:"last_name,omitempty"`
	DateOfBirth       *string `json:"date_of_birth,omitempty"`
	Email             *string `json:"email,omitempty"`
	PAN               *string `json:"pan,omitempty"`
	ConsentMessage    string  `json:"consent_message"`
	ConsentAcceptance string  `json:"consent_acceptance"`
	DeviceType        string  `json:"device_type"`
	OTP               string  `json:"otp"`
	Timestamp         string  `json:"timestamp"`
	DeviceIP          string  `json:"device_ip"`
	DeviceID          *string `json:"device_id,omitempty"`
	ReportType        *int    `json:"report_type,omitempty"`
}

// Validation regexes derived from section 1.4.1. Kept lenient where the spec's
// own regex is overly broad (e.g. consent_text) so we don't reject valid input;
// the upstream performs authoritative validation.
var (
	mobileRe    = regexp.MustCompile(`^(?:\+91\s?|0)?[6-9]\d{9}$`)
	panRe       = regexp.MustCompile(`^[A-Za-z]{5}\d{4}[A-Za-z]$`)
	emailRe     = regexp.MustCompile(`^[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+\.[a-zA-Z]{2,}$`)
	otpRe       = regexp.MustCompile(`^\d{1,6}$`)
	timestampRe = regexp.MustCompile(`^\d{8}-\d{2}:\d{2}:\d{2}$`) // DDMMYYYY-HH:MM:SS
)

// validate enforces the mandatory/conditional rules from section 1.4.1. Returns
// a per-field detail map suitable for apperr.NewValidationWith.
func (in *CreditAnalyticsInput) validate() map[string]string {
	d := map[string]string{}

	if strings.TrimSpace(in.ClientRefNum) == "" {
		d["client_ref_num"] = "is required"
	} else if len(in.ClientRefNum) > 45 {
		d["client_ref_num"] = "must be at most 45 characters"
	}

	if !mobileRe.MatchString(in.MobileNo) {
		d["mobile_no"] = "must be a valid 10-digit Indian mobile number"
	}

	// name_lookup is 0/1 when provided. If it's 1, first/last name must NOT be
	// passed; if it's 0 or absent and the caller wants to supply a name, both
	// first and last name are expected. We only enforce the explicit rules.
	if in.NameLookup != nil && (*in.NameLookup != 0 && *in.NameLookup != 1) {
		d["name_lookup"] = "must be 0 or 1"
	}

	if in.PAN != nil && !panRe.MatchString(*in.PAN) {
		d["pan"] = "must match format ABCDE1234F"
	}
	if in.Email != nil && !emailRe.MatchString(*in.Email) {
		d["email"] = "must be a valid email address"
	}

	if strings.TrimSpace(in.ConsentMessage) == "" {
		d["consent_message"] = "is required"
	}
	ca := strings.ToLower(strings.TrimSpace(in.ConsentAcceptance))
	if ca != "yes" && ca != "no" {
		d["consent_acceptance"] = "must be 'yes' or 'no'"
	}

	dt := strings.ToLower(strings.TrimSpace(in.DeviceType))
	if dt != "web" && dt != "mobile" {
		d["device_type"] = "must be 'web' or 'mobile'"
	}
	// device_id is conditional-mandatory only when device_type == mobile.
	if dt == "mobile" && (in.DeviceID == nil || strings.TrimSpace(*in.DeviceID) == "") {
		d["device_id"] = "is required when device_type is 'mobile'"
	}

	if !otpRe.MatchString(in.OTP) {
		d["otp"] = "must be numeric (max 6 digits)"
	}
	if !timestampRe.MatchString(in.Timestamp) {
		d["timestamp"] = "must be DDMMYYYY-HH:MM:SS"
	}
	if strings.TrimSpace(in.DeviceIP) == "" {
		d["device_ip"] = "is required"
	}
	if in.ReportType != nil && (*in.ReportType < 0 || *in.ReportType > 4) {
		d["report_type"] = "must be one of 0,1,2,3,4"
	}

	return d
}

// Request validates the input, calls the Digitap API, persists a row capturing
// the request and the full upstream response, then returns the stored row.
//
// Upstream HTTP statuses are mapped to typed app errors:
//   - 200: success (result_code 101/102/103 all returned as a persisted row)
//   - 400: validation -> apperr.Validation
//   - 401: auth        -> apperr.Unauthorized
//   - 422: tradeline   -> apperr.PanFailure
//   - 5xx / other:     -> apperr (Bad Gateway, surfaced as 500 by the handler)
//
// A row is persisted before any error is returned, so failed upstream calls are
// still queryable in the DB.
func (s *CreditAnalyticsService) Request(ctx context.Context, accountID int64, in CreditAnalyticsInput) (*models.CreditAnalyticsRequest, error) {
	if details := in.validate(); len(details) > 0 {
		return nil, apperr.NewValidationWith("invalid credit-analytics request", details)
	}

	reqBody, err := json.Marshal(in)
	if err != nil {
		return nil, fmt.Errorf("marshal credit-analytics request: %w", err)
	}

	env, httpStatus, err := s.client.Request(ctx, in)
	if err != nil {
		// Network/transport failure — persist what we have, then surface the error.
		row := s.buildRow(accountID, in, reqBody)
		_ = s.persist(ctx, row)
		return nil, apperr.NewValidation("credit-analytics provider unreachable: " + err.Error())
	}

	row := s.buildRow(accountID, in, reqBody)
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
		return row, nil
	case httpStatus == http.StatusBadRequest:
		return row, apperr.NewValidation(env.Message)
	case httpStatus == http.StatusUnauthorized:
		return row, apperr.NewUnauthorized(env.Message)
	case httpStatus == http.StatusUnprocessableEntity:
		return row, apperr.NewPanFailure(env.Message)
	case httpStatus == http.StatusServiceUnavailable, httpStatus == http.StatusInternalServerError:
		return row, apperr.NewValidation(fmt.Sprintf("digitap upstream error (%d): %s", httpStatus, env.Message))
	default:
		return row, apperr.NewValidation(fmt.Sprintf("digitap upstream error (%d): %s", httpStatus, env.Message))
	}
}

// buildRow assembles a model row from the input payload (before the upstream
// response fields are filled in by the caller).
func (s *CreditAnalyticsService) buildRow(accountID int64, in CreditAnalyticsInput, reqBody []byte) *models.CreditAnalyticsRequest {
	return &models.CreditAnalyticsRequest{
		AccountID:    &accountID,
		ClientRefNum: in.ClientRefNum,
		MobileNo:     in.MobileNo,
		RequestBody:  reqBody,
	}
}

func (s *CreditAnalyticsService) persist(ctx context.Context, row *models.CreditAnalyticsRequest) error {
	if err := s.repo.Create(ctx, row); err != nil {
		return fmt.Errorf("persist credit-analytics request: %w", err)
	}
	return nil
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
