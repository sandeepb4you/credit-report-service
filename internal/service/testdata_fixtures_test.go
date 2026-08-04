package service

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// These tests guard the hand-authored Digitap sample fixtures under
// internal/digitap/testdata/. The fixtures are full envelopes used to seed
// demo/test databases (see docs/examples/load_sample_report_815.sql) and must
// stay parseable by the real insights parser. Each assertion encodes its
// persona's intent so a future edit can't silently break the field a screen
// depends on.
//
// The fixtures live next to the digitap client (they are upstream-shaped
// responses); these tests reach across packages only to read files, and run
// them through the same unexported parseReportInsights the service uses.

const fixturesDir = "../digitap/testdata"

// loadEnvelope reads a fixture file and returns the `result` object (what the
// service stores verbatim in response_body) plus the top-level result_code.
func loadEnvelope(t *testing.T, name string) (json.RawMessage, *int) {
	t.Helper()
	path := filepath.Join(fixturesDir, name)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var env struct {
		ResultCode *int            `json:"result_code"`
		Result     json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal %s: %v", path, err)
	}
	return env.Result, env.ResultCode
}

// parseEnvelopeResult parses the `result` object through the real insights
// parser, failing on any error.
func parseEnvelopeResult(t *testing.T, result json.RawMessage) *ReportInsights {
	t.Helper()
	ins, err := parseReportInsights(result)
	if err != nil {
		t.Fatalf("parseReportInsights: %v", err)
	}
	return ins
}

func TestFixture_BlendedJourney_690(t *testing.T) {
	result, code := loadEnvelope(t, "journey05a_blended_690.json")
	if code == nil || *code != 101 {
		t.Fatalf("result_code = %v, want 101", code)
	}
	ins := parseEnvelopeResult(t, result)

	if ins.TotalAccountCount != 3 {
		t.Errorf("accounts = %d, want 3 (card + card + personal loan)", ins.TotalAccountCount)
	}
	if ins.ActiveAccountCount != 3 {
		t.Errorf("active = %d, want 3", ins.ActiveAccountCount)
	}
	// Two cards: 36k/80k + 12k/50k = 48k/130k ≈ 36.9% -> mid-band (B).
	if ins.CardUtilizationPercent < 30 || ins.CardUtilizationPercent > 50 {
		t.Errorf("utilisation = %.1f, want in 30–50%% band (blended)", ins.CardUtilizationPercent)
	}
	if ins.OnTimePaymentPercent != 100 {
		t.Errorf("onTime = %.1f, want 100 (clean blended file)", ins.OnTimePaymentPercent)
	}
	if ins.EnquiryCount180Days != 2 {
		t.Errorf("enquiries180 = %d, want 2", ins.EnquiryCount180Days)
	}
	if ins.DerogatoryAccounts != 0 {
		t.Errorf("derogatory = %d, want 0", ins.DerogatoryAccounts)
	}
}

func TestFixture_Derogatory_540(t *testing.T) {
	result, code := loadEnvelope(t, "journey05c_derogatory_540.json")
	if code == nil || *code != 101 {
		t.Fatalf("result_code = %v, want 101", code)
	}
	ins := parseEnvelopeResult(t, result)

	// The whole point of this fixture: a written-off + a settled tradeline.
	if ins.DerogatoryAccounts != 2 {
		t.Errorf("derogatory = %d, want 2 (written-off card + settled loan)", ins.DerogatoryAccounts)
	}
	// Status "97" (written-off) is inactive; settled loan is "00" closed.
	// Only the active HDFC card remains.
	if ins.ActiveAccountCount != 1 {
		t.Errorf("active = %d, want 1", ins.ActiveAccountCount)
	}
	if ins.TotalAccountCount != 3 {
		t.Errorf("accounts = %d, want 3", ins.TotalAccountCount)
	}
}

func TestFixture_NoRecord_102(t *testing.T) {
	result, code := loadEnvelope(t, "no_record_102.json")
	if code == nil || *code != 102 {
		t.Fatalf("result_code = %v, want 102", code)
	}
	// A 102 response carries no record: `result` is JSON null, which must NOT
	// be parseable as an insights object (the service stores nothing and the
	// report's credit_score stays nil).
	if !isNull(result) {
		t.Errorf("result = %s, want null for 102 no-record", string(result))
	}
}

func TestFixture_NameMissing_103(t *testing.T) {
	result, code := loadEnvelope(t, "name_missing_103.json")
	if code == nil || *code != 103 {
		t.Fatalf("result_code = %v, want 103", code)
	}
	if !isNull(result) {
		t.Errorf("result = %s, want null for 103 name-missing", string(result))
	}
}

// isNull reports whether a json.RawMessage is the JSON literal null (or empty).
func isNull(b json.RawMessage) bool {
	return len(b) == 0 || string(b) == "null"
}

func TestFixture_ThinFileNoScore_101(t *testing.T) {
	result, code := loadEnvelope(t, "thin_file_no_score_101.json")
	if code == nil || *code != 101 {
		t.Fatalf("result_code = %v, want 101", code)
	}
	ins := parseEnvelopeResult(t, result)

	// A real record but no SCORE block: confirm it is genuinely absent so the
	// service's extractBureauScore yields nil (not a fabricated 0).
	var wrapper struct {
		ResultJSON struct {
			INProfileResponse struct {
				SCORE *struct {
					BureauScore string `json:"BureauScore"`
				} `json:"SCORE"`
			} `json:"INProfileResponse"`
		} `json:"result_json"`
	}
	if err := json.Unmarshal(result, &wrapper); err != nil {
		t.Fatalf("re-parse for SCORE block: %v", err)
	}
	if wrapper.ResultJSON.INProfileResponse.SCORE != nil {
		t.Errorf("thin-file fixture must not carry a SCORE block, got %+v",
			wrapper.ResultJSON.INProfileResponse.SCORE)
	}
	if ins.TotalAccountCount != 1 {
		t.Errorf("accounts = %d, want 1 (thin file)", ins.TotalAccountCount)
	}
	if ins.ActiveAccountCount != 1 {
		t.Errorf("active = %d, want 1", ins.ActiveAccountCount)
	}
}
