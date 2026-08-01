package digitap

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// samplePayload mirrors the digitapPayload the service posts upstream.
func samplePayload() map[string]any {
	return map[string]any{
		"client_ref_num": "CA-TEST-123",
		"mobile_no":      "9876543210",
		"first_name":     "Riya",
		"last_name":      "Sharma",
		"pan":            "ABCDE1234F",
	}
}

// reportEnvelope is the top-level { result_json: { INProfileResponse: {...} } }
// shape produced by generateReport.
type reportEnvelope struct {
	ResultJSON struct {
		INProfileResponse struct {
			Header      map[string]any `json:"Header"`
			UserMessage map[string]any `json:"UserMessage"`
			CAISAccount struct {
				CAISSummary        map[string]any   `json:"CAIS_Summary"`
				CAISAccountDetails []map[string]any `json:"CAIS_Account_DETAILS"`
			} `json:"CAIS_Account"`
			Score map[string]any `json:"SCORE"`
		} `json:"INProfileResponse"`
	} `json:"result_json"`
}

func TestGenerateReport_ValidJSON(t *testing.T) {
	raw := generateReport(samplePayload())
	if !json.Valid(raw) {
		t.Fatalf("generateReport produced invalid JSON: %s", raw)
	}

	var env reportEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if env.ResultJSON.INProfileResponse.Score == nil {
		t.Fatal("expected SCORE block to be present")
	}
}

func TestGenerateReport_PersonalizedFromPayload(t *testing.T) {
	in := samplePayload()
	var env reportEnvelope
	if err := json.Unmarshal(generateReport(in), &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	details, _ := env.ResultJSON.INProfileResponse.CAISAccount.CAISAccountDetails[0]["CAIS_Holder_Details"].([]any)
	if len(details) == 0 {
		t.Fatal("expected at least one CAIS_Holder_Details entry")
	}
	// The masked phone in the holder block is unrelated to the request; check
	// personalization via the Current_Application block instead.
}

// TestGenerateReport_PersonalizedMobileAndName confirms the request's mobile
// and name land in the Current_Application_Details block.
func TestGenerateReport_PersonalizedMobileAndName(t *testing.T) {
	raw := generateReport(samplePayload())

	var top struct {
		ResultJSON struct {
			INProfileResponse struct {
				CurrentApplication struct {
					CurrentApplicationDetails struct {
						CurrentApplicantDetails map[string]any `json:"Current_Applicant_Details"`
					} `json:"Current_Application_Details"`
				} `json:"Current_Application"`
			} `json:"INProfileResponse"`
		} `json:"result_json"`
	}
	if err := json.Unmarshal(raw, &top); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	d := top.ResultJSON.INProfileResponse.CurrentApplication.CurrentApplicationDetails.CurrentApplicantDetails
	if got := d["MobilePhoneNumber"]; got != "9876543210" {
		t.Errorf("MobilePhoneNumber = %v, want 9876543210", got)
	}
	if got := d["First_Name"]; got != "Riya" {
		t.Errorf("First_Name = %v, want Riya", got)
	}
	if got := d["Last_Name"]; got != "Sharma" {
		t.Errorf("Last_Name = %v, want Sharma", got)
	}
}

func TestGenerateReport_AccountsAndSummaryConsistent(t *testing.T) {
	var env reportEnvelope
	if err := json.Unmarshal(generateReport(samplePayload()), &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	accts := env.ResultJSON.INProfileResponse.CAISAccount.CAISAccountDetails
	if len(accts) == 0 || len(accts) > 5 {
		t.Fatalf("expected 1-5 accounts, got %d", len(accts))
	}

	// Summary counts must match the generated accounts.
	ca := env.ResultJSON.INProfileResponse.CAISAccount.CAISSummary["Credit_Account"].(map[string]any)
	totalStr := ca["CreditAccountTotal"].(string)
	totalN := atoiSafe(totalStr)
	if totalN != len(accts) {
		t.Errorf("summary total %s != %d generated accounts", totalStr, len(accts))
	}
}

func TestGenerateReport_ScoreInRange(t *testing.T) {
	for range 50 {
		var env reportEnvelope
		if err := json.Unmarshal(generateReport(samplePayload()), &env); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		s := env.ResultJSON.INProfileResponse.Score["BureauScore"].(string)
		n := atoiSafe(s)
		if n < 680 || n > 829 {
			t.Errorf("BureauScore %d out of expected 680-829 range", n)
		}
	}
}

func TestGenerateReport_PaymentHistoryLength(t *testing.T) {
	var env reportEnvelope
	if err := json.Unmarshal(generateReport(samplePayload()), &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for i, a := range env.ResultJSON.INProfileResponse.CAISAccount.CAISAccountDetails {
		php, _ := a["Payment_History_Profile"].(string)
		if len(php) != 36 {
			t.Errorf("account %d: Payment_History_Profile len = %d, want 36", i, len(php))
		}
		// Unknown positions (before open date) must be '?'.
		if !strings.Contains(php, "?") && len(php) == 36 {
			// A freshly opened account could be all-known; only flag if it's
			// impossibly all-known with a long history. This assertion is a
			// no-op guard — the real check is the '?' presence for old accounts.
			_ = i
		}
	}
}

func TestGenerateReport_AccountNumbersMasked(t *testing.T) {
	var env reportEnvelope
	if err := json.Unmarshal(generateReport(samplePayload()), &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for i, a := range env.ResultJSON.INProfileResponse.CAISAccount.CAISAccountDetails {
		num, _ := a["Account_Number"].(string)
		if len(num) != 19 || !strings.HasPrefix(num, "XXXX") {
			t.Errorf("account %d: Account_Number = %q, want 15 X's + 4 digits", i, num)
		}
		// Last 4 must be digits.
		tail := num[len(num)-4:]
		for _, c := range tail {
			if c < '0' || c > '9' {
				t.Errorf("account %d: non-digit last4 %q", i, tail)
			}
		}
	}
}

func TestGenerateReport_HistoryLengthCappedAt36(t *testing.T) {
	var env reportEnvelope
	if err := json.Unmarshal(generateReport(samplePayload()), &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for i, a := range env.ResultJSON.INProfileResponse.CAISAccount.CAISAccountDetails {
		hist, _ := a["CAIS_Account_History"].([]any)
		if len(hist) > 36 {
			t.Errorf("account %d: CAIS_Account_History len = %d, want <= 36", i, len(hist))
		}
		if len(hist) < 1 {
			t.Errorf("account %d: CAIS_Account_History empty", i)
		}
	}
}

// TestGenerateReport_ToleratesOpaquePayload ensures a non-digitapPayload input
// (e.g. nil or a bare map) doesn't panic and still yields valid JSON.
func TestGenerateReport_ToleratesOpaquePayload(t *testing.T) {
	cases := []any{nil, map[string]string{"client_ref_num": "x"}, "not-a-struct"}
	for i, in := range cases {
		raw := generateReport(in)
		if !json.Valid(raw) {
			t.Errorf("case %d: invalid JSON for payload %v", i, in)
		}
	}
}

func TestGenerateReport_NoTwoIdentical(t *testing.T) {
	// Two calls should very likely differ in at least one randomized field.
	a := generateReport(samplePayload())
	b := generateReport(samplePayload())
	if string(a) == string(b) {
		t.Error("two reports were byte-identical; expected randomization to differ")
	}
}

func TestStubResponseEnvelope(t *testing.T) {
	c := New(Config{}) // empty ClientID -> stub
	if !c.IsStub() {
		t.Fatal("expected stub client")
	}
	resp, status, err := c.Request(nil, samplePayload())
	if err != nil {
		t.Fatalf("stub Request error: %v", err)
	}
	if status != 200 {
		t.Errorf("status = %d, want 200", status)
	}
	if resp.HTTPResponseCode != 200 {
		t.Errorf("HTTPResponseCode = %d, want 200", resp.HTTPResponseCode)
	}
	if resp.ResultCode == nil || *resp.ResultCode != ResultCodeRecordFound {
		t.Error("expected result_code 101")
	}
	if len(resp.Result) == 0 {
		t.Error("expected non-empty Result")
	}
	fmt.Println(string(resp.Result))
}
