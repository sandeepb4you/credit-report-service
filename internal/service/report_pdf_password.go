package service

import (
	"bytes"
	"fmt"
	"strings"
	"time"

	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
)

// ReportPDFPassword builds the password that opens a credit-report PDF:
// the PAN in upper case followed by the date of birth as DDMMYYYY, no separator.
//
//	ABCDE1234F + 24091991  ->  "ABCDE1234F24091991"
//
// Both halves come from facts the holder already knows and a thief of the file
// generally does not, which is the point: the report is emailed as an
// attachment, and mailboxes get forwarded, backed up and breached. It follows
// the convention Indian lenders already use for statements, so the instruction
// reads as familiar rather than arbitrary.
//
// The exact rule has to be stated wherever the file is handed over — a
// password-protected PDF whose password nobody can derive is a lost report. See
// [ReportPDFPasswordHint].
//
// Returns an error rather than a weaker password when either half is missing.
// Falling back to PAN alone would silently downgrade every report belonging to
// an account with no date of birth, which is precisely the population a phone
// signup produces.
func ReportPDFPassword(pan string, dob *time.Time) (string, error) {
	p := strings.ToUpper(strings.TrimSpace(pan))
	if p == "" {
		return "", fmt.Errorf("report pdf password: no PAN on file")
	}
	if dob == nil {
		return "", fmt.Errorf("report pdf password: no date of birth on file")
	}
	return p + dob.UTC().Format("02012006"), nil
}

// ReportPDFPasswordHint is the sentence shown to the user next to the download
// or in the covering email. It describes the rule without containing the
// password, so it is safe in an email body that may be quoted or forwarded.
const ReportPDFPasswordHint = "The PDF is password-protected. " +
	"Open it with your PAN in capitals followed by your date of birth as DDMMYYYY — " +
	"for example ABCDE1234F24091991."

// EncryptReportPDF returns pdf encrypted so that password is required to open
// it.
//
// Applied before upload, not on the way out to email: the object then sits
// encrypted in S3 as well, so bucket-level access alone does not yield a
// readable credit report. S3's own server-side encryption protects the bytes at
// rest from outside the account; this protects them from inside it.
//
// The owner password is set to the same value as the user password on purpose.
// Leaving it empty would let any reader strip the restrictions, and inventing a
// second secret would mean storing one — the point here is that we hold nothing
// that opens the file.
func EncryptReportPDF(pdf []byte, password string) ([]byte, error) {
	if password == "" {
		return nil, fmt.Errorf("encrypt report pdf: empty password")
	}
	conf := model.NewAESConfiguration(password, password, 256)
	out := &bytes.Buffer{}
	if err := api.Encrypt(bytes.NewReader(pdf), out, conf); err != nil {
		return nil, fmt.Errorf("encrypt report pdf: %w", err)
	}
	return out.Bytes(), nil
}
