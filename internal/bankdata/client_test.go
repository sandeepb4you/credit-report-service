package bankdata

import (
	"context"
	"encoding/json"
	"testing"
)

// TestClient_StubByDefault confirms empty credentials yield a stub client that
// never needs I/O — the convention every external-capability package follows.
func TestClient_StubByDefault(t *testing.T) {
	c := New(Config{BaseURL: "https://example.invalid/"})
	if !c.IsStub() {
		t.Fatalf("empty client-id should produce a stub client")
	}
	c2 := New(Config{BaseURL: "https://example.invalid/", ClientID: "x", ClientSecret: "y"})
	if c2.IsStub() {
		t.Fatalf("non-empty client-id should produce a real client")
	}
}

// TestStub_GenerateURL verifies the stub returns a success envelope with a url
// and request_id the later calls will accept.
func TestStub_GenerateURL(t *testing.T) {
	c := New(Config{})
	resp, status, err := c.GenerateURL(context.Background(), GenerateURLRequest{
		ClientRefNum: "BS-123",
	})
	if err != nil {
		t.Fatalf("GenerateURL error: %v", err)
	}
	if status != 200 {
		t.Errorf("status = %d, want 200", status)
	}
	if resp.Status != "success" {
		t.Errorf("status field = %q, want success", resp.Status)
	}
	if resp.URL == "" || resp.RequestID == "" {
		t.Errorf("expected non-empty url and request_id, got %+v", resp)
	}
}

// TestStub_StatusCheck_ReportGenerated confirms the stub reports the report as
// ready (so a full dev flow completes on the first status check).
func TestStub_StatusCheck_ReportGenerated(t *testing.T) {
	c := New(Config{})
	resp, _, err := c.StatusCheck(context.Background(), "req-stub-1")
	if err != nil {
		t.Fatalf("StatusCheck error: %v", err)
	}
	if len(resp.TxnStatus) != 1 {
		t.Fatalf("expected 1 txn status, got %d", len(resp.TxnStatus))
	}
	ts := resp.TxnStatus[0]
	if ts.Code != CodeReportGenerated {
		t.Errorf("code = %q, want %q", ts.Code, CodeReportGenerated)
	}
	if ts.TxnID == "" {
		t.Errorf("expected a non-empty txn_id to retrieve the report with")
	}
}

// TestStub_RetrieveReport_HasResult confirms the stub returns a non-empty
// categorised report payload under result.
func TestStub_RetrieveReport_HasResult(t *testing.T) {
	c := New(Config{})
	resp, _, err := c.RetrieveReport(context.Background(), "txn-stub-1")
	if err != nil {
		t.Fatalf("RetrieveReport error: %v", err)
	}
	if resp.Status != "success" {
		t.Errorf("status = %q, want success", resp.Status)
	}
	if len(resp.Result) == 0 {
		t.Fatalf("expected a non-empty report payload under result")
	}
	// The stub result is a JSON object; confirm it decodes and carries the
	// expected top-level keys the UI relies on.
	var report map[string]any
	if err := json.Unmarshal(resp.Result, &report); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}
	for _, key := range []string{"summary", "salary", "categories"} {
		if _, ok := report[key]; !ok {
			t.Errorf("report missing key %q", key)
		}
	}
}

// TestStub_InstitutionList is a smoke test for the placeholder endpoint.
func TestStub_InstitutionList(t *testing.T) {
	c := New(Config{})
	resp, _, err := c.InstitutionList(context.Background())
	if err != nil {
		t.Fatalf("InstitutionList error: %v", err)
	}
	if len(resp.Institutions) == 0 {
		t.Errorf("expected at least one stub institution")
	}
}
