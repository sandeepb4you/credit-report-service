// Package bankdata — request/response types for the Digitap Bank Data PDF UI
// API. The package doc and client live in client.go; this file holds only the
// data shapes so they can be referenced by the client and stub without cycles.
package bankdata

import "encoding/json"

// API endpoints (paths relative to BaseURL). See the v1.20 doc, sections 4-7.
const (
	PathGenerateURL     = "/generateurl"
	PathStatusCheck     = "/statuscheck"
	PathRetrieveReport  = "/retrievereport"
	PathInstitutionList = "/institutions" // §7.1 — not "/institutionlist"
)

// Report format. Always JSON: the report is stored verbatim in a JSONB column,
// and an SLA default of xlsx would hand us a spreadsheet. The subtype is
// configurable (statement.digitap.report-subtype) because which subtypes a
// client may pull is per-contract — asking for one outside it is
// InvalidReportType — and empty omits it so Digitap applies the SLA default.
// See doc Appendix C.
const ReportTypeJSON = "json"

// DestinationStatementUpload is the destination for the PDF-upload UI (§4.4).
const DestinationStatementUpload = "statementupload"

// CallbackTypeTransactionComplete is the header value Digitap sends on the
// transaction-complete callback. The doc warns the same callback URL may carry
// other types in the future, so the handler checks this before processing.
const CallbackTypeTransactionComplete = "TRANSACTION_COMPLETE"

// ---- Generate URL --------------------------------------------------------

// GenerateURLRequest is the payload for POST /bank-data/generateurl
// (Header-Based auth: client_name is not required, so it's omitted here).
// Field names use the snake_case contract exactly as Digitap expects.
type GenerateURLRequest struct {
	ClientRefNum      string `json:"client_ref_num"`
	TxnCompletedCBURL string `json:"txn_completed_cburl"`
	StartMonth        string `json:"start_month,omitempty"` // YYYY-MM
	EndMonth          string `json:"end_month,omitempty"`   // YYYY-MM
	InstitutionID     string `json:"institution_id,omitempty"`
	Destination       string `json:"destination,omitempty"` // "statementupload"
	// ReturnURL is where the browser goes when the user is done. Must be
	// omitted for the Android WebView flow, which learns of completion through
	// the DigitapBS.onFinish JavaScript bridge instead (Appendix F).
	ReturnURL       string           `json:"return_url,omitempty"`
	EmployerDetails []EmployerDetail `json:"employer_details,omitempty"`
	// AcceptancePolicy is one of atLeastOneTransactionInRange,
	// atLeastOneTransactionPerMonthInRange, exactStatementRange (Appendix E).
	// Empty applies the client configuration held at Digitap.
	AcceptancePolicy string `json:"acceptance_policy,omitempty"`
	// MultiAccountSupportRequired is "1" to let one transaction carry several
	// accounts, possibly at several banks (§1.2 B).
	MultiAccountSupportRequired string `json:"multi_account_support_required,omitempty"`
}

// EmployerDetail is one entry in the optional employer_details list. Either
// Name or CIN helps Digitap flag salary transactions more accurately.
type EmployerDetail struct {
	Name string `json:"name,omitempty"`
	CIN  string `json:"CIN,omitempty"`
}

// GenerateURLResponse is the Digitap envelope returned by Generate URL.
// On success: Status=="success" with URL/Expires/RequestID populated.
// On error:   Status=="error" with Code/Msg populated.
type GenerateURLResponse struct {
	Status    string `json:"status"`     // "success" | "error"
	URL       string `json:"url"`        // Digitap UI URL to hand the client
	Expires   string `json:"expires"`    // "2020-04-10T07:01:35.054" — no zone; see ParseExpires
	RequestID string `json:"request_id"` // Digitap correlation id
	Code      string `json:"code,omitempty"`
	Msg       string `json:"msg,omitempty"`
}

// ---- Status Check --------------------------------------------------------

// StatusCheckRequest is the payload for POST /bank-data/statuscheck.
type StatusCheckRequest struct {
	RequestID string `json:"request_id"`
}

// StatusCheckResponse carries the per-transaction statuses for a request_id.
// One request_id can own several transactions — a multi-account upload, or a
// user who failed once and tried again from the same link — so a single
// failed entry does not decide the outcome. See service.pickDigitapOutcome.
type StatusCheckResponse struct {
	Status    string      `json:"status"` // API-level: "success" | "error"
	RequestID string      `json:"request_id"`
	TxnStatus []TxnStatus `json:"txn_status"`
	Code      string      `json:"code,omitempty"`
	Msg       string      `json:"msg,omitempty"`
}

// TxnStatus is one transaction's status within a status-check response.
type TxnStatus struct {
	TxnID  string `json:"txn_id"`
	Status string `json:"status"` // "Success" | "Failure" | "Error" | "InProgress"
	Code   string `json:"code"`   // "ReportGenerated", "TxnExpired", etc.
	Msg    string `json:"msg"`
}

// Per-transaction status values (§5.5).
const (
	TxnStatusSuccess    = "Success"
	TxnStatusFailure    = "Failure"
	TxnStatusError      = "Error"
	TxnStatusInProgress = "InProgress"
)

// Status-check txn codes (doc §4.8 / §5.5). ReportGenerated means the report is
// ready to retrieve; TxnInitiated / TxnProcessing are still moving. Anything
// arriving with a Failure or Error status is terminal.
const (
	CodeReportGenerated = "ReportGenerated"
	CodeTxnInitiated    = "TxnInitiated"
	CodeTxnProcessing   = "TxnProcessing"
	CodeTxnExpired      = "TxnExpired"
	CodeUserCancelled   = "UserCancelled"
	// CodeTxnNotCompleted is Retrieve Report's answer for a transaction that
	// has not finished (§6.7) — "not yet", not a failure.
	CodeTxnNotCompleted = "TxnNotCompleted"
)

// ---- Retrieve Report -----------------------------------------------------

// RetrieveReportRequest is the payload for POST /bank-data/retrievereport.
type RetrieveReportRequest struct {
	TxnID         string `json:"txn_id"`
	ReportType    string `json:"report_type"`              // "json" | "xlsx"
	ReportSubtype string `json:"report_subtype,omitempty"` // json: type1|type2|type3; empty = SLA default
}

// RetrieveReportResponse is the Digitap envelope for the retrieve call. The
// Result is the raw report payload (its shape is Digitap's schema, so callers
// treat it as opaque JSON and store it verbatim).
//
// On success the doc (§6.5) says the response body IS the report, not an
// envelope around it; on error it is {"status":"error","code","message"} —
// "message" here, where the other endpoints say "msg" (§6.6). IsError is the
// only reliable test, and Result is empty whenever it is true.
type RetrieveReportResponse struct {
	Status  string          `json:"status"` // "error" on failure; the report's own field, if any, otherwise
	Result  json.RawMessage `json:"result"`
	Code    string          `json:"code,omitempty"`
	Msg     string          `json:"msg,omitempty"`
	Message string          `json:"message,omitempty"`
}

// IsError reports whether Digitap answered with an error envelope.
func (r *RetrieveReportResponse) IsError() bool { return r.Status == "error" }

// ErrorText is the upstream's description of an error, whichever key it used.
func (r *RetrieveReportResponse) ErrorText() string {
	if r.Msg != "" {
		return r.Msg
	}
	return r.Message
}

// ---- Callback webhook ----------------------------------------------------

// CallbackEvent is the body Digitap POSTs to txn_completed_cburl when the user
// finishes (success or failure) on the upload UI. See doc Appendix A.
//
// The body is unauthenticated, so nothing in it is trusted as an outcome: it
// only names a request_id, and the service asks Status Check what happened.
type CallbackEvent struct {
	TxnID        string `json:"txn_id"`
	Status       string `json:"status"` // "Success" | "Failure"
	Code         string `json:"code"`
	Message      string `json:"message"` // the sample's key; the table calls it "msg"
	ClientRefNum string `json:"client_ref_num"`
	RequestID    string `json:"request_id"`
}

// ---- Institution list (not wired into the service) ------------------------

// InstitutionTypeStatement is the Institution List "type" for statement upload.
const InstitutionTypeStatement = "Statement"

// InstitutionListRequest is the payload for POST /bank-data/institutions.
type InstitutionListRequest struct {
	Type string `json:"type"`
}

// Institution is one supported bank in the Institution List API response.
// The id is what Generate URL's institution_id takes, as a string.
type Institution struct {
	ID       int    `json:"id"`
	Name     string `json:"name"`
	InstType string `json:"inst_type"` // "bank" | "other"
}

// InstitutionListResponse is the envelope for POST /bank-data/institutions.
type InstitutionListResponse struct {
	Status       string        `json:"status"`
	Institutions []Institution `json:"data"`
	Code         string        `json:"code,omitempty"`
	Msg          string        `json:"msg,omitempty"`
	Message      string        `json:"message,omitempty"`
}
