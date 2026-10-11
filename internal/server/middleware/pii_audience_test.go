package middleware

import (
	"strings"
	"testing"
)

// A retargeting export, as POST /admin/audiences/{id}/export returns it: the
// whole file as one string. The request logger logs response bodies, and shape
// masking alone would scrub the phone and email but leave the names standing —
// a list of customers' names in a log line.
func TestMaskJSON_AudienceExportLosesTheWholeFile(t *testing.T) {
	body := `{"filename":"myscorr_listJ_all_2026-10-11.csv",` +
		`"csv":"user_id,first_name,last_name,phone,email\n41,Priya,Ramaswamy,+919812345678,priya@example.com\n",` +
		`"selected":1,"included":1,"leftOut":0}`
	out := string(maskJSON([]byte(body)))

	for _, leak := range []string{"Priya", "Ramaswamy", "9812345678", "priya@example.com"} {
		if strings.Contains(out, leak) {
			t.Errorf("%q survived masking:\n%s", leak, out)
		}
	}
	// The counts are what make the line useful, and they carry nothing personal.
	if !strings.Contains(out, `"included":1`) || !strings.Contains(out, "myscorr_listJ_all") {
		t.Errorf("the export's shape should stay readable:\n%s", out)
	}
}
