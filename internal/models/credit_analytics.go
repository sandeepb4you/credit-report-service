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
	RequestID    *string         `json:"requestId"    db:"request_id"`
	ResultCode   *int            `json:"resultCode"   db:"result_code"`
	HTTPStatus   *int            `json:"httpStatus"   db:"http_status"`
	Message      *string         `json:"message"      db:"message"`
	RequestBody  json.RawMessage `json:"requestBody"  db:"request_body"`
	ResponseBody json.RawMessage `json:"responseBody" db:"response_body"`
	CreatedAt    time.Time       `json:"createdAt"    db:"created_at"`
}
