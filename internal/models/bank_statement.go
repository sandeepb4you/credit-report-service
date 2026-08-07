package models

import (
	"encoding/json"
	"time"
)

// BankStatement is the row model for bank_statements: one row per uploaded
// bank-statement PDF. The raw PDF bytes, the extracted text layer, and the
// derived analysis are all kept in-row (BYTEA / TEXT / JSONB), mirroring how
// credit_analytics_requests stores its request/response verbatim.
//
// Because PDF parsing runs asynchronously, a row may be read while still
// 'processing' — in which case Analysis, ExtractedText, and the period columns
// are nil. The status field is the source of truth for whether analysis is
// available.
type BankStatement struct {
	ID        int64 `json:"id" db:"id"`
	AccountID int64 `json:"accountId" db:"account_id"`

	// Provider records which flow produced this row: 'local' (the client
	// uploaded a PDF we parse in-process) or 'digitap' (the user uploaded to
	// Digitap's UI and we fetched the report). Drives the poll fallback and how
	// the client interprets the analysis payload.
	Provider string `json:"provider" db:"provider"`

	Filename string `json:"filename" db:"filename"`
	MimeType string `json:"mimeType" db:"mime_type"`
	Status   string `json:"status" db:"status"`
	PDFBytes []byte `json:"-" db:"pdf_bytes"` // never serialized to JSON

	// ExtractedText is the PDF text layer. Empty until the worker runs.
	// Local-provider rows only; digitap rows leave this null.
	ExtractedText string `json:"extractedText,omitempty" db:"extracted_text"`
	// Analysis is the derived metrics (salary, EMI, categories, ...). Nil until
	// the row reaches status 'completed'. For local rows it's our Analysis
	// struct; for digitap rows it's Digitap's report JSON, stored verbatim.
	Analysis json.RawMessage `json:"analysis,omitempty" db:"analysis"`
	// ErrorMessage is set only when Status == 'failed'.
	ErrorMessage string `json:"errorMessage,omitempty" db:"error_message"`

	TransactionCount *int       `json:"transactionCount,omitempty" db:"transaction_count"`
	PeriodStart      *time.Time `json:"periodStart,omitempty" db:"period_start"`
	PeriodEnd        *time.Time `json:"periodEnd,omitempty" db:"period_end"`

	// Digitap-flow fields. Populated for provider='digitap' rows; null otherwise.
	// RequestID/RedirectURL/URLExpiresAt are set at initiation; TxnID is filled
	// in when the callback or status-check first names the transaction.
	RequestID    string     `json:"requestId,omitempty" db:"request_id"`
	TxnID        string     `json:"txnId,omitempty" db:"txn_id"`
	RedirectURL  string     `json:"redirectUrl,omitempty" db:"redirect_url"`
	URLExpiresAt *time.Time `json:"urlExpiresAt,omitempty" db:"url_expires_at"`

	CreatedAt   time.Time  `json:"createdAt" db:"created_at"`
	CompletedAt *time.Time `json:"completedAt,omitempty" db:"completed_at"`
}

// Bank statement status values. Kept as untyped constants so they compare
// directly with the string column without an import cycle.
const (
	BankStatementStatusProcessing = "processing"
	BankStatementStatusCompleted  = "completed"
	BankStatementStatusFailed     = "failed"
)

// Bank statement provider values.
const (
	BankStatementProviderLocal   = "local"
	BankStatementProviderDigitap = "digitap"
)
