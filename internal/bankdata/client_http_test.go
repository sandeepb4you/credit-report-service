package bankdata

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// recorded is one request as the fake Digitap server saw it.
type recorded struct {
	path string
	user string
	pass string
	body map[string]any
}

// fakeDigitap answers every request with reply and records what was sent, so
// these tests pin the bytes on the wire against the v1.20 doc rather than
// against the stub, which only ever agrees with itself.
func fakeDigitap(t *testing.T, reply string) (*Client, *recorded) {
	t.Helper()
	got := &recorded{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.path = r.URL.Path
		got.user, got.pass, _ = r.BasicAuth()
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &got.body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, reply)
	}))
	t.Cleanup(srv.Close)
	c := New(Config{BaseURL: srv.URL + "/bank-data/", ClientID: "cid", ClientSecret: "secret"})
	return c, got
}

// Header-based auth (§2.2.2) is Basic client_id:client_secret, and paths join
// the base without doubling the slash.
func TestClient_BasicAuthAndPath(t *testing.T) {
	c, got := fakeDigitap(t, `{"status":"success","request_id":"r1","txn_status":[]}`)
	if _, _, err := c.StatusCheck(context.Background(), "r1"); err != nil {
		t.Fatalf("StatusCheck: %v", err)
	}
	if got.path != "/bank-data/statuscheck" {
		t.Errorf("path = %q, want /bank-data/statuscheck", got.path)
	}
	if got.user != "cid" || got.pass != "secret" {
		t.Errorf("basic auth = %q:%q, want cid:secret", got.user, got.pass)
	}
	if got.body["request_id"] != "r1" {
		t.Errorf("body = %v, want request_id r1", got.body)
	}
}

// §6.5: a successful Retrieve Report body IS the report. All of it has to be
// kept — including a top-level "result" key, which the old decoder would have
// taken on its own and dropped the rest.
func TestClient_RetrieveReport_KeepsWholeBody(t *testing.T) {
	body := `{"result":{"a":1},"customer_info":{"name":"X"},"accounts":[]}`
	c, got := fakeDigitap(t, body)
	resp, _, err := c.RetrieveReport(context.Background(), "t1")
	if err != nil {
		t.Fatalf("RetrieveReport: %v", err)
	}
	if resp.IsError() {
		t.Fatalf("a report was read as an error: %+v", resp)
	}
	if string(resp.Result) != body {
		t.Errorf("Result = %s, want the body verbatim", resp.Result)
	}
	if got.body["report_type"] != "json" {
		t.Errorf("report_type = %v, want json", got.body["report_type"])
	}
	// No subtype configured: omitted, so Digitap applies the SLA default.
	if _, sent := got.body["report_subtype"]; sent {
		t.Errorf("report_subtype sent as %v with none configured", got.body["report_subtype"])
	}
}

// §6.6: the error envelope says "message", not "msg". It must never be stored
// as though it were the report.
func TestClient_RetrieveReport_ErrorEnvelope(t *testing.T) {
	c, _ := fakeDigitap(t,
		`{"status":"error","code":"TxnNotFound","message":"We could not find the Digitap Transaction referred by the client"}`)
	resp, _, err := c.RetrieveReport(context.Background(), "t1")
	if err != nil {
		t.Fatalf("RetrieveReport: %v", err)
	}
	if !resp.IsError() {
		t.Fatalf("error envelope not recognised")
	}
	if len(resp.Result) != 0 {
		t.Errorf("error envelope carried a Result: %s", resp.Result)
	}
	if resp.Code != "TxnNotFound" || resp.ErrorText() == "" {
		t.Errorf("code/text = %q/%q", resp.Code, resp.ErrorText())
	}
}

func TestClient_RetrieveReport_ConfiguredSubtype(t *testing.T) {
	c, got := fakeDigitap(t, `{}`)
	c.cfg.ReportSubtype = "type3"
	if _, _, err := c.RetrieveReport(context.Background(), "t1"); err != nil {
		t.Fatalf("RetrieveReport: %v", err)
	}
	if got.body["report_subtype"] != "type3" {
		t.Errorf("report_subtype = %v, want type3", got.body["report_subtype"])
	}
}

// §7: the path is /institutions, "type" is mandatory, and the list is "data"
// with integer ids.
func TestClient_InstitutionList(t *testing.T) {
	c, got := fakeDigitap(t,
		`{"status":"success","data":[{"id":7,"name":"HDFC Bank","inst_type":"bank"}]}`)
	resp, _, err := c.InstitutionList(context.Background())
	if err != nil {
		t.Fatalf("InstitutionList: %v", err)
	}
	if got.path != "/bank-data/institutions" {
		t.Errorf("path = %q, want /bank-data/institutions", got.path)
	}
	if got.body["type"] != "Statement" {
		t.Errorf("type = %v, want Statement", got.body["type"])
	}
	if len(resp.Institutions) != 1 || resp.Institutions[0].ID != 7 || resp.Institutions[0].InstType != "bank" {
		t.Errorf("institutions = %+v", resp.Institutions)
	}
}

// Generate URL carries the fields the doc defines, and none that are empty.
func TestClient_GenerateURL_Payload(t *testing.T) {
	c, got := fakeDigitap(t,
		`{"status":"success","url":"https://u","expires":"2020-04-10T07:01:35.054","request_id":"r"}`)
	_, _, err := c.GenerateURL(context.Background(), GenerateURLRequest{
		ClientRefNum:                "BS-1",
		TxnCompletedCBURL:           "https://cb",
		StartMonth:                  "2026-03",
		EndMonth:                    "2026-09",
		Destination:                 DestinationStatementUpload,
		MultiAccountSupportRequired: "1",
	})
	if err != nil {
		t.Fatalf("GenerateURL: %v", err)
	}
	want := map[string]any{
		"client_ref_num": "BS-1", "txn_completed_cburl": "https://cb",
		"start_month": "2026-03", "end_month": "2026-09",
		"destination": "statementupload", "multi_account_support_required": "1",
	}
	for k, v := range want {
		if got.body[k] != v {
			t.Errorf("%s = %v, want %v", k, got.body[k], v)
		}
	}
	// The Android WebView flow depends on return_url being absent, not "".
	for _, k := range []string{"return_url", "acceptance_policy", "institution_id"} {
		if _, sent := got.body[k]; sent {
			t.Errorf("%s sent while empty", k)
		}
	}
}

func TestParseExpires(t *testing.T) {
	cases := []struct {
		in   string
		want time.Time
		ok   bool
	}{
		// The doc's own sample: no zone, read as UTC.
		{"2020-04-10T07:01:35.054", time.Date(2020, 4, 10, 7, 1, 35, 54_000_000, time.UTC), true},
		{"2020-04-10T07:01:35", time.Date(2020, 4, 10, 7, 1, 35, 0, time.UTC), true},
		{"2020-04-10T07:01:35Z", time.Date(2020, 4, 10, 7, 1, 35, 0, time.UTC), true},
		{"2020-04-10T12:31:35+05:30", time.Date(2020, 4, 10, 7, 1, 35, 0, time.UTC), true},
		{"", time.Time{}, false},
		{"tomorrow", time.Time{}, false},
	}
	for _, tc := range cases {
		got, ok := ParseExpires(tc.in)
		if ok != tc.ok || (ok && !got.Equal(tc.want)) {
			t.Errorf("ParseExpires(%q) = %v, %v; want %v, %v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}
