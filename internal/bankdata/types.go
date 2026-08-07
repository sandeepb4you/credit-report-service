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
	PathInstitutionList = "/institutionlist"
)

// Default report format/subtype: JSON + type2 = simple report plus per-
// transaction categorisation (salary, expense buckets). This gives us the
// richest single-call payload. See doc Appendix C.
const (
	ReportTypeJSON  = "json"
	ReportSubtypeT2 = "type2" // simple report + categorisation per transaction
)

// CallbackTypeTransactionComplete is the header value Digitap sends on the
// transaction-complete callback. The doc warns the same callback URL may carry
// other types in the future, so the handler checks this before processing.
const CallbackTypeTransactionComplete = "TRANSACTION_COMPLETE"

// ---- Generate URL --------------------------------------------------------

// GenerateURLRequest is the payload for POST /bank-data/generateurl
// (Header-Based auth: client_name is not required, so it's omitted here).
// Field names use the snake_case contract exactly as Digitap expects.
type GenerateURLRequest struct {
	ClientRefNum      string           `json:"client_ref_num"`
	TxnCompletedCBURL string           `json:"txn_completed_cburl"`
	StartMonth        string           `json:"start_month,omitempty"` // YYYY-MM
	EndMonth          string           `json:"end_month,omitempty"`   // YYYY-MM
	InstitutionID     string           `json:"institution_id,omitempty"`
	Destination       string           `json:"destination,omitempty"` // "statementupload"
	ReturnURL         string           `json:"return_url,omitempty"`
	EmployerDetails   []EmployerDetail `json:"employer_details,omitempty"`
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
	Expires   string `json:"expires"`    // ISO-8601 timestamp; URL expiry
	RequestID string `json:"request_id"` // Digitap correlation id
	Code      string `json:"code,omitempty"`
	Msg       string `json:"msg,omitempty"`
}

// ---- Status Check --------------------------------------------------------

// StatusCheckRequest is the payload for POST /bank-data/statuscheck.
type StatusCheckRequest struct {
	RequestID string `json:"request_id"`
}

// StatusCheckResponse carries the per-transaction status for a request_id.
// A request_id can have multiple txns (multi-account upload); we scan for the
// first terminal one.
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

// Status-check txn codes (doc §4.8 / §5). ReportGenerated means the report is
// ready to retrieve; TxnExpired (and any Failure/Error) is terminal-failed.
const (
	CodeReportGenerated = "ReportGenerated"
	CodeTxnExpired      = "TxnExpired"
)

// ---- Retrieve Report -----------------------------------------------------

// RetrieveReportRequest is the payload for POST /bank-data/retrievereport.
type RetrieveReportRequest struct {
	TxnID         string `json:"txn_id"`
	ReportType    string `json:"report_type"`    // "json" | "xlsx"
	ReportSubtype string `json:"report_subtype"` // json: type1|type2|type3
}

// RetrieveReportResponse is the Digitap envelope for the retrieve call. The
// Result is the raw report payload (its shape is Digitap's schema, so callers
// treat it as opaque JSON and store it verbatim).
type RetrieveReportResponse struct {
	Status string          `json:"status"` // "success" | "error"
	Result json.RawMessage `json:"result"`
	Code   string          `json:"code,omitempty"`
	Msg    string          `json:"msg,omitempty"`
}

// ---- Callback webhook ----------------------------------------------------

// CallbackEvent is the body Digitap POSTs to txn_completed_cburl when the user
// finishes (success or failure) on the upload UI. See doc Appendix A.
type CallbackEvent struct {
	TxnID        string `json:"txn_id"`
	Status       string `json:"status"` // "Success" | "Failure"
	Code         string `json:"code"`
	Message      string `json:"message"`
	ClientRefNum string `json:"client_ref_num"`
	RequestID    string `json:"request_id"`
}

// ---- Institution list (placeholder, not wired) ---------------------------

// Institution is one supported bank in the Institution List API response.
type Institution struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// InstitutionListResponse is the envelope for POST /bank-data/institutionlist.
type InstitutionListResponse struct {
	Status       string        `json:"status"`
	Institutions []Institution `json:"institutions"`
	Code         string        `json:"code,omitempty"`
	Msg          string        `json:"msg,omitempty"`
}
