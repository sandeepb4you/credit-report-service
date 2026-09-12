package middleware

import (
	"strings"
	"testing"
)

// The presigned report-PDF link, as GET /credit-analytics/reports/{id}/pdf returns
// it. Signature and credential are the shape of real ones — 64 hex characters and
// a dated scope — because that shape is what the redactor matches on.
const presignedPDFBody = `{"url":"https://myscorr-credit-reports.s3.ap-south-1.amazonaws.com/credit-reports/2/19.pdf` +
	`?X-Amz-Algorithm=AWS4-HMAC-SHA256` +
	`&X-Amz-Credential=ASIAYJBDQRS5EXAMPLE%2F20260912%2Fap-south-1%2Fs3%2Faws4_request` +
	`&X-Amz-Date=20260912T071022Z&X-Amz-Expires=600` +
	`&X-Amz-Security-Token=IQoJb3JpZ2luX2VjEExampleTokenValue` +
	`&X-Amz-SignedHeaders=host` +
	`&X-Amz-Signature=4f1d9c2b8a7e6f5d4c3b2a1908f7e6d5c4b3a29180716253443526170819aabb",` +
	`"expiresInSeconds":600,"passwordHint":"Your PAN in capitals plus your date of birth as DDMMYYYY"}`

// A presigned URL is a credential: whoever holds it can download that object until
// it expires, with no account and no token. The request logger logs response
// bodies, so an unmasked one puts working download links for customers' credit
// reports into the log.
//
// Today the 1KB body cap happens to cut most of these links off before the
// signature — but that is a coincidence of string lengths, not a control, and a
// shorter password hint would undo it.
func TestMaskJSON_PresignedURLLosesItsSignature(t *testing.T) {
	out := string(maskJSON([]byte(presignedPDFBody)))

	for _, secret := range []string{
		"4f1d9c2b8a7e6f5d4c3b2a1908f7e6d5c4b3a29180716253443526170819aabb",
		"ASIAYJBDQRS5EXAMPLE",
		"IQoJb3JpZ2luX2VjEExampleTokenValue",
	} {
		if strings.Contains(out, secret) {
			t.Errorf("presigned credential survived masking: %q in\n%s", secret, out)
		}
	}
}

// Only the secret-bearing parameters go. Which object was downloaded, and when
// the link expires, are exactly what makes the log line worth keeping when
// someone asks why a download failed.
func TestMaskJSON_PresignedURLKeepsWhatIsUseful(t *testing.T) {
	out := string(maskJSON([]byte(presignedPDFBody)))

	for _, kept := range []string{
		"myscorr-credit-reports",
		"credit-reports/2/19.pdf",
		"X-Amz-Expires=600",
		// The parameter names survive so a reader can see the link was signed at
		// all, rather than finding a URL that looks unauthenticated.
		"X-Amz-Signature=",
		"X-Amz-Credential=",
	} {
		if !strings.Contains(out, kept) {
			t.Errorf("masking removed something useful: %q missing from\n%s", kept, out)
		}
	}
}

// The same body can reach the logger unparsed — a non-JSON payload, or a body the
// logger fell back on. Shape redaction has to hold there too.
func TestMaskRaw_PresignedURL(t *testing.T) {
	raw := "GET https://myscorr-credit-reports.s3.ap-south-1.amazonaws.com/credit-reports/2/19.pdf" +
		"?X-Amz-Credential=ASIAYJBDQRS5EXAMPLE%2F20260912%2Fap-south-1%2Fs3%2Faws4_request" +
		"&X-Amz-Signature=4f1d9c2b8a7e6f5d4c3b2a1908f7e6d5c4b3a29180716253443526170819aabb HTTP/1.1"

	out := string(maskRaw([]byte(raw)))

	if strings.Contains(out, "4f1d9c2b8a7e6f5d4c3b2a1908f7e6d5c4b3a29180716253443526170819aabb") {
		t.Errorf("signature survived raw masking:\n%s", out)
	}
	if strings.Contains(out, "ASIAYJBDQRS5EXAMPLE") {
		t.Errorf("credential survived raw masking:\n%s", out)
	}
	if !strings.Contains(out, "credit-reports/2/19.pdf") {
		t.Errorf("raw masking ate the object key:\n%s", out)
	}
}
