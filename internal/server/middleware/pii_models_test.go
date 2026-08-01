package middleware

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"credit-report-service/internal/models"
)

// TestMaskJSON_AgainstRealModels marshals the actual response model structs
// (Account, KYCRecord) and confirms maskJSON redacts every PII value they
// expose. This guards against silent drift if a new PII field is added to a
// model without updating sensitiveKeys.
func TestMaskJSON_AgainstRealModels(t *testing.T) {
	email := "ada@lovelace.io"
	phone := "9876543210"
	fn, ln := "Ada", "Lovelace"
	dob := time.Date(1815, 12, 10, 0, 0, 0, 0, time.UTC)
	panName := "Rahul Sharma"
	aadhaarLast4 := "1234"
	aadhaarRef := "REFXYZ"

	acc := models.Account{
		ID: 1, Status: "ACTIVE", Role: "user",
		PrimaryEmail: &email, PrimaryPhone: &phone,
		FirstName: &fn, LastName: &ln, DateOfBirth: &dob,
	}
	kyc := models.KYCRecord{
		ID: 1, AccountID: 1, PANNumber: "ABCDE1234F", PANName: &panName,
		AadhaarLast4: &aadhaarLast4, AadhaarReference: &aadhaarRef,
	}

	accJSON, _ := json.Marshal(acc)
	kycJSON, _ := json.Marshal(kyc)

	// Every plaintext PII value that appears in the marshalled models.
	piiValues := []string{
		email, phone, fn, ln, "1815", "ABCDE1234F", panName, aadhaarLast4, aadhaarRef,
	}

	for _, src := range []struct {
		name string
		raw  []byte
	}{
		{"Account", accJSON},
		{"KYCRecord", kycJSON},
	} {
		masked := maskJSON(src.raw)
		for _, p := range piiValues {
			if strings.Contains(string(masked), p) {
				t.Errorf("%s: PII %q leaked through masker:\n  raw:    %s\n  masked: %s",
					src.name, p, src.raw, masked)
			}
		}
	}
}
