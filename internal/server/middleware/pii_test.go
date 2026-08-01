package middleware

import (
	"encoding/json"
	"strings"
	"testing"
)

// Tests for the PII masking layer. These are the safety contract for logging
// request/response bodies: every sensitive field must be redacted regardless of
// case or nesting, and recognizable sensitive value-shapes must be scrubbed even
// under non-sensitive keys.

func TestMaskJSON_Credentials(t *testing.T) {
	cases := []struct {
		name string
		in   string
	}{
		{"password", `{"email":"user@example.com","password":"hunter2pass"}`},
		{"otp", `{"email":"user@example.com","otp":"123456"}`},
		{"token", `{"token":"eyJhbGci.eyJzdWIi.eftts"}`},
		{"idToken", `{"idToken":"eyJhbGci.eyJzdWIi.eftts"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := maskJSON([]byte(tc.in))
			assertNoPII(t, tc.in, out, []string{extractFirstValue(tc.in, tc.name)})
		})
	}
}

func TestMaskJSON_ContactFields(t *testing.T) {
	in := `{"email":"a@b.com","phone":"9876543210","mobileNo":"9876543210","mobilePhoneNumber":"9876543210"}`
	out := maskJSON([]byte(in))
	assertNoPII(t, in, out, []string{"a@b.com", "9876543210"})
}

func TestMaskJSON_PanAndAadhaar(t *testing.T) {
	in := `{"pan":"ABCDE1234F","panName":"Rahul Sharma","aadhaarLast4":"1234","aadhaarReference":"XYZ123"}`
	out := maskJSON([]byte(in))
	assertNoPII(t, in, out, []string{"ABCDE1234F", "Rahul Sharma", "1234", "XYZ123"})
}

func TestMaskJSON_NamesAndDOB(t *testing.T) {
	in := `{"firstName":"Ada","lastName":"Lovelace","dateOfBirth":"1815-12-10","middleName1":"X"}`
	out := maskJSON([]byte(in))
	assertNoPII(t, in, out, []string{"Ada", "Lovelace", "1815-12-10"})
}

func TestMaskJSON_CaseInsensitiveKeys(t *testing.T) {
	// Key matching must be case- and separator-insensitive.
	in := `{"Email":"A@B.COM","DateOfBirth":"1990-01-01","date-of-birth":"1991-02-02"}`
	out := maskJSON([]byte(in))
	assertNoPII(t, in, out, []string{"A@B.COM", "1990-01-01", "1991-02-02"})
}

func TestMaskJSON_NestedAndArrays(t *testing.T) {
	// PII nested inside objects and arrays must still be caught.
	in := `{"items":[{"email":"deep@nest.com","name":"Deep"}],"meta":{"pan":"ZZZZZ9999Z"}}`
	out := maskJSON([]byte(in))
	assertNoPII(t, in, out, []string{"deep@nest.com", "Deep", "ZZZZZ9999Z"})
}

func TestMaskJSON_NonSensitiveValuePreserved(t *testing.T) {
	// A normal field under a non-sensitive key is left intact.
	in := `{"status":"PENDING","id":42,"verified":true}`
	out := maskJSON([]byte(in))
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("invalid JSON out: %v", err)
	}
	if got["status"] != "PENDING" {
		t.Errorf("status altered: %v", got["status"])
	}
	if got["verified"] != true {
		t.Errorf("verified altered: %v", got["verified"])
	}
}

func TestMaskJSON_DefenseInDepth_ShapeInUnknownKey(t *testing.T) {
	// A PAN / email / JWT under a novel key name must still be scrubbed by the
	// value-shape redactors.
	in := `{"customField":"ABCDE1234F","notes":"reach me at leak@contoso.com"}`
	out := maskJSON([]byte(in))
	assertNoPII(t, in, out, []string{"ABCDE1234F", "leak@contoso.com"})
}

func TestMaskJSON_BearerTokenShape(t *testing.T) {
	// A JWT-shaped string anywhere must be redacted.
	jwt := "Bearer eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiI0MiJ9.eftts123456"
	in := `{"auth":"` + jwt + `"}`
	out := maskJSON([]byte(in))
	if strings.Contains(string(out), "eyJhbGci") {
		t.Errorf("JWT body leaked: %s", out)
	}
}

func TestMaskJSON_Empty(t *testing.T) {
	if got := maskJSON(nil); got != nil {
		t.Errorf("maskJSON(nil) = %v, want nil", got)
	}
	if got := maskJSON([]byte("")); len(got) != 0 {
		t.Errorf("maskJSON(empty) = %v, want empty", got)
	}
}

func TestMaskJSON_InvalidJSONFallsBackToRawMask(t *testing.T) {
	// Garbage body that still contains a PAN must have the PAN scrubbed.
	in := []byte(`not json pan=ABCDE1234F done`)
	out := maskJSON(in)
	if strings.Contains(string(out), "ABCDE1234F") {
		t.Errorf("PAN leaked through raw-mask path: %s", out)
	}
}

func TestMaskJSON_OutputIsValidJSON(t *testing.T) {
	in := `{"email":"a@b.com","password":"p","n":3}`
	out := maskJSON([]byte(in))
	if !json.Valid(out) {
		t.Fatalf("masked output is not valid JSON: %s", out)
	}
}

func TestTruncateBytes(t *testing.T) {
	// Under the limit: unchanged.
	small := []byte("short")
	if got := truncateBytes(small, 100); string(got) != "short" {
		t.Errorf("truncateBytes altered a short input: %q", got)
	}
	// Over the limit: capped, with a truncation marker.
	big := []byte(strings.Repeat("x", 500))
	got := truncateBytes(big, 10)
	if !strings.HasSuffix(string(got), "…(+truncated)") {
		t.Errorf("missing truncation marker: %q", got)
	}
	// Marker added beyond the cap.
	if len(got) <= 10 {
		t.Errorf("truncateBytes produced nothing beyond the cap: %q", got)
	}
}

func TestMaskedBody_OmitsWhitespaceOnly(t *testing.T) {
	if got := maskedBody([]byte("   \n\t ")); got != nil {
		t.Errorf("maskedBody on whitespace = %v, want nil", got)
	}
	if got := maskedBody(nil); got != nil {
		t.Errorf("maskedBody(nil) = %v, want nil", got)
	}
}

// assertNoPII fails the test if any of the plaintext secrets appear in the
// masked output.
func assertNoPII(t *testing.T, original string, masked []byte, secrets []string) {
	t.Helper()
	out := string(masked)
	for _, s := range secrets {
		if s == "" {
			continue
		}
		if strings.Contains(out, s) {
			t.Errorf("PII leaked into masked output:\n  original: %s\n  masked:   %s\n  leaked:   %q", original, out, s)
		}
	}
	// Every masked sensitive value should be replaced with the marker.
	if strings.Contains(original, "@") && !strings.Contains(out, maskValue) {
		t.Errorf("expected mask marker %q in output, got: %s", maskValue, out)
	}
}

// extractFirstValue pulls the value of the first occurrence of key from a flat
// JSON object string, for seeding the "must not leak" list. Returns "" if it
// can't find it.
func extractFirstValue(jsonStr, key string) string {
	needle := `"` + key + `":"`
	i := strings.Index(jsonStr, needle)
	if i < 0 {
		return ""
	}
	rest := jsonStr[i+len(needle):]
	j := strings.IndexByte(rest, '"')
	if j < 0 {
		return ""
	}
	return rest[:j]
}
