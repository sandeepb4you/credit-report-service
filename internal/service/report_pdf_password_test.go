package service

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
)

func dob(t *testing.T, s string) *time.Time {
	t.Helper()
	d, err := time.Parse("2006-01-02", s)
	if err != nil {
		t.Fatal(err)
	}
	return &d
}

func TestReportPDFPassword(t *testing.T) {
	pw, err := ReportPDFPassword("abcde1234f", dob(t, "1991-09-24"))
	if err != nil {
		t.Fatal(err)
	}
	// PAN upper-cased, DOB as DDMMYYYY, nothing between them. This is the string
	// the covering email tells the user to type, so it is a contract, not a
	// detail: changing the shape silently locks people out of their own report.
	if pw != "ABCDE1234F24091991" {
		t.Errorf("password = %q, want ABCDE1234F24091991", pw)
	}
}

func TestReportPDFPassword_SingleDigitDayAndMonthArePadded(t *testing.T) {
	pw, err := ReportPDFPassword("ABCDE1234F", dob(t, "1980-04-02"))
	if err != nil {
		t.Fatal(err)
	}
	if pw != "ABCDE1234F02041980" {
		t.Errorf("password = %q, want ABCDE1234F02041980 (zero-padded)", pw)
	}
}

// Refusing beats weakening. A PAN-only fallback would silently downgrade every
// report belonging to an account with no date of birth — which is exactly what a
// phone signup produces until PAN verification supplies one.
func TestReportPDFPassword_RefusesWhenAHalfIsMissing(t *testing.T) {
	if _, err := ReportPDFPassword("", dob(t, "1991-09-24")); err == nil {
		t.Error("no PAN must be an error, not a weaker password")
	}
	if _, err := ReportPDFPassword("ABCDE1234F", nil); err == nil {
		t.Error("no DOB must be an error, not a PAN-only password")
	}
}

// The hint has to describe the rule without ever containing a real password:
// it goes into an email body that gets forwarded and quoted.
func TestReportPDFPasswordHint_DescribesTheRule(t *testing.T) {
	for _, want := range []string{"PAN", "DDMMYYYY", "password"} {
		if !bytes.Contains([]byte(ReportPDFPasswordHint), []byte(want)) {
			t.Errorf("hint does not mention %q: %s", want, ReportPDFPasswordHint)
		}
	}
}

// The real check: encrypt a genuine PDF, then prove the wrong password fails and
// the right one succeeds. A unit test on the password string alone would pass
// happily while producing files nobody can open.
func TestEncryptReportPDF_RequiresThePassword(t *testing.T) {
	plain := newTestPDF(t)

	const pw = "ABCDE1234F24091991"
	enc, err := EncryptReportPDF(plain, pw)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if len(enc) == 0 {
		t.Fatal("encrypted output is empty")
	}
	if bytes.Equal(enc, plain) {
		t.Fatal("output is byte-identical to the input; nothing was encrypted")
	}

	// No password at all.
	if err := api.Validate(bytes.NewReader(enc), model.NewDefaultConfiguration()); err == nil {
		t.Error("encrypted PDF validated with no password")
	}
	// Wrong password.
	wrongConf := model.NewAESConfiguration("WRONG000000000000", "WRONG000000000000", 256)
	if err := api.Validate(bytes.NewReader(enc), wrongConf); err == nil {
		t.Error("encrypted PDF validated with the wrong password")
	}
	// Right password.
	rightConf := model.NewAESConfiguration(pw, pw, 256)
	if err := api.Validate(bytes.NewReader(enc), rightConf); err != nil {
		t.Errorf("encrypted PDF did not validate with the correct password: %v", err)
	}
}

func TestEncryptReportPDF_RejectsEmptyPassword(t *testing.T) {
	if _, err := EncryptReportPDF(newTestPDF(t), ""); err == nil {
		t.Error("an empty password must be refused, not applied")
	}
}

// newTestPDF returns a real single-page PDF from testdata.
//
// A fixture rather than a synthesized document: pdfcpu's own demo helpers build
// an xref table with no page tree, and a hand-rolled %PDF header is not a PDF
// its encrypter will accept. The point of these tests is that encryption works
// on a genuine file, so the input has to be one.
func newTestPDF(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "sample.pdf"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return b
}
