package models

import (
	"encoding/json"
	"time"
)

// CreditAnalyticsRequest is the row model for credit_analytics_requests: one
// row per outbound call to the Digitap /credit_analytics/request API. The
// request payload and the full upstream response are stored verbatim as JSONB
// (the Experian bureau payload is large and schema-versioned upstream).
type CreditAnalyticsRequest struct {
	ID           int64           `json:"id"           db:"id"`
	AccountID    *int64          `json:"accountId"    db:"account_id"`
	ClientRefNum string          `json:"clientRefNum" db:"client_ref_num"`
	MobileNo     string          `json:"mobileNo"     db:"mobile_no"`
	// IdempotencyKey is the caller's replay key, unique per account. Nil when
	// the caller sent none, which every row predating the column also is.
	IdempotencyKey *string `json:"-" db:"idempotency_key"`

	// ReuseCount counts the times this stored report was served in place of a
	// fresh bureau pull, and LastReusedAt when that last happened. Internal
	// accounting — how often reuse fires and what it saves — so both stay out of
	// the JSON; a client learns what it needs from the X-Report-Reused header.
	ReuseCount   int        `json:"-" db:"reuse_count"`
	LastReusedAt *time.Time `json:"-" db:"last_reused_at"`

	// ReusedFromReportID names the live pull this row copied its data from, and is
	// nil when this row IS a live pull.
	ReusedFromReportID *int64 `json:"-" db:"reused_from_report_id"`

	// DataFetchedAt is when the data here came off the bureau, as distinct from
	// CreatedAt, which is when the row was written. They match on a live pull. A
	// copy inherits it, which is what stops a copy restarting the reuse window —
	// otherwise refreshing just inside it forever would keep minting rows that
	// each looked fresh while the underlying pull aged without limit.
	DataFetchedAt time.Time `json:"-" db:"data_fetched_at"`
	RequestID    *string         `json:"requestId"    db:"request_id"`
	ResultCode   *int            `json:"resultCode"   db:"result_code"`
	HTTPStatus   *int            `json:"httpStatus"   db:"http_status"`
	Message      *string         `json:"message"      db:"message"`
	RequestBody  json.RawMessage `json:"requestBody"  db:"request_body"`
	ResponseBody json.RawMessage `json:"responseBody" db:"response_body"`
	// CreditScore is the bureau score (SCORE.BureauScore) lifted out of the
	// response at write time. Nil when the pull failed or returned no record.
	CreditScore *int64 `json:"creditScore" db:"credit_score"`
	// ResultPDFURL is the s3:// URI of the stored PDF report, not a URL anyone
	// can follow: the bucket is private, so reads are presigned at request time.
	// Digitap returns a link that lives about an hour (result_pdf); we download
	// it, encrypt it with the holder's PAN + date of birth and upload it
	// asynchronously. Nil until that completes, if it failed (best-effort), or
	// if no password could be built — an unprotectable report is not stored.
	// Mirrors the creditScore lift-out.
	ResultPDFURL *string   `json:"resultPdfUrl,omitempty" db:"result_pdf_url"`
	CreatedAt    time.Time `json:"createdAt"   db:"created_at"`
}
