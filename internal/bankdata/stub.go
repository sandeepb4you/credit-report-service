package bankdata

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"sync/atomic"
)

// stubSeq generates monotonic ids for synthesized stub responses so each call
// is internally consistent (e.g. the request_id from GenerateURL is the one
// StatusCheck accepts) without a real upstream.
var stubSeq uint64

func nextStubID(prefix string) string {
	n := atomic.AddUint64(&stubSeq, 1)
	return fmt.Sprintf("%s-stub-%d", prefix, n)
}

// stubCall dispatches a stub response by endpoint path. Free function (Go
// disallows generic methods). It never performs I/O.
func stubCall[T any](path string, payload any, decode func([]byte) (*T, error)) (*T, int, error) {
	slog.Debug("bank-data stub response (no live upstream configured)", "path", path)

	var raw []byte
	switch path {
	case PathGenerateURL:
		raw = stubGenerateURL(payload)
	case PathStatusCheck:
		raw = stubStatusCheck(payload)
	case PathRetrieveReport:
		raw = stubRetrieveReport(payload)
	case PathInstitutionList:
		raw = stubInstitutionList()
	default:
		raw = []byte(`{"status":"error","code":"InternalError","msg":"unknown stub path"}`)
	}
	out, err := decode(raw)
	if err != nil {
		return nil, 0, err
	}
	return out, 200, nil
}

func stubGenerateURL(payload any) []byte {
	// Carry the request_id on both the Generate URL and the later StatusCheck
	// so the stub flow is internally consistent end-to-end.
	reqID := nextStubID("req")
	b, _ := json.Marshal(GenerateURLResponse{
		Status:    "success",
		URL:       "https://digitap-stub.example/bank-data/ui?request_id=" + reqID,
		Expires:   "2099-12-31T23:59:59Z",
		RequestID: reqID,
	})
	return b
}

func stubStatusCheck(payload any) []byte {
	// Extract the request_id so the synthesized txn_id is deterministic per call.
	var req StatusCheckRequest
	_ = json.Unmarshal(mustMarshal(payload), &req)
	txnID := nextStubID("txn")
	if req.RequestID == "" {
		req.RequestID = nextStubID("req")
	}
	b, _ := json.Marshal(StatusCheckResponse{
		Status:    "success",
		RequestID: req.RequestID,
		TxnStatus: []TxnStatus{{
			TxnID:  txnID,
			Status: "Success",
			Code:   CodeReportGenerated,
			Msg:    "The report has been successfully generated for the transaction",
		}},
	})
	return b
}

func stubRetrieveReport(payload any) []byte {
	// A canned type2-style categorised report. The shape mirrors what the
	// statement/stub.go analyzer produces so a digitap row and a local row look
	// similar in dev. Callers treat this as opaque JSON.
	report := map[string]any{
		"summary": map[string]any{
			"total_credits":     171000.00,
			"total_debits":      19700.00,
			"net_cash_flow":     151300.00,
			"transaction_count": 4,
		},
		"salary": map[string]any{
			"description": "INFOSYS LTD SALARY",
			"amount":      85000.00,
			"occurrences": 2,
		},
		"categories": []map[string]any{
			{"category": "emi", "count": 2, "total": 37000.00},
			{"category": "upi", "count": 1, "total": 1200.00},
			{"category": "card", "count": 1, "total": 1500.00},
		},
	}
	b, _ := json.Marshal(RetrieveReportResponse{
		Status: "success",
		Result: mustMarshal(report),
	})
	return b
}

func stubInstitutionList() []byte {
	b, _ := json.Marshal(InstitutionListResponse{
		Status: "success",
		Institutions: []Institution{
			{ID: "1", Name: "HDFC Bank"},
			{ID: "2", Name: "ICICI Bank"},
			{ID: "3", Name: "State Bank of India"},
		},
	})
	return b
}

func mustMarshal(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		return []byte("{}")
	}
	return b
}
