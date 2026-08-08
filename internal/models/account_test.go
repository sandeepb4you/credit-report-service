package models

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestNewKYCStatus_NoRecord(t *testing.T) {
	st := NewKYCStatus(nil)
	if st.Status != KycNotSubmitted {
		t.Errorf("status = %q, want %q", st.Status, KycNotSubmitted)
	}
	if st.PANSubmitted || st.PANVerified {
		t.Errorf("panSubmitted=%v panVerified=%v, want both false", st.PANSubmitted, st.PANVerified)
	}
	if st.PANLast4 != "" {
		t.Errorf("panLast4 = %q, want empty", st.PANLast4)
	}
	if st.Done() {
		t.Error("Done() = true for an account with no PAN on file")
	}
}

func TestNewKYCStatus_PendingRecord(t *testing.T) {
	created := time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC)
	updated := created.Add(time.Hour)
	st := NewKYCStatus(&KYCRecord{
		PANNumber: "ABCDE1234F",
		Status:    KycPending,
		CreatedAt: created,
		UpdatedAt: updated,
	})

	if st.Status != KycPending {
		t.Errorf("status = %q, want %q", st.Status, KycPending)
	}
	if !st.PANSubmitted {
		t.Error("panSubmitted = false, want true")
	}
	if st.PANVerified {
		t.Error("panVerified = true for an unverified record")
	}
	if st.PANLast4 != "234F" {
		t.Errorf("panLast4 = %q, want %q", st.PANLast4, "234F")
	}
	if st.VerifiedAt != nil {
		t.Errorf("verifiedAt = %v, want nil", st.VerifiedAt)
	}
	if st.CreatedAt == nil || !st.CreatedAt.Equal(created) {
		t.Errorf("createdAt = %v, want %v", st.CreatedAt, created)
	}
	if st.UpdatedAt == nil || !st.UpdatedAt.Equal(updated) {
		t.Errorf("updatedAt = %v, want %v", st.UpdatedAt, updated)
	}
	if st.Done() {
		t.Error("Done() = true for a PENDING record")
	}
}

func TestNewKYCStatus_VerifiedRecord(t *testing.T) {
	verified := time.Date(2026, 7, 2, 9, 0, 0, 0, time.UTC)
	st := NewKYCStatus(&KYCRecord{
		PANNumber:   "ABCDE1234F",
		PANVerified: true,
		Status:      KycVerified,
		VerifiedAt:  &verified,
	})

	if !st.Done() {
		t.Errorf("Done() = false for status=%q panVerified=%v", st.Status, st.PANVerified)
	}
	if st.VerifiedAt == nil || !st.VerifiedAt.Equal(verified) {
		t.Errorf("verifiedAt = %v, want %v", st.VerifiedAt, verified)
	}
}

// A row whose status says VERIFIED but whose pan_verified flag is still false
// is inconsistent; Done() must not report KYC as complete off the status alone.
func TestKYCStatus_DoneRequiresBothSignals(t *testing.T) {
	cases := []struct {
		name   string
		status KYCStatus
		want   bool
	}{
		{"both set", KYCStatus{Status: KycVerified, PANVerified: true}, true},
		{"status only", KYCStatus{Status: KycVerified}, false},
		{"flag only", KYCStatus{Status: KycPending, PANVerified: true}, false},
		{"rejected", KYCStatus{Status: KycRejected}, false},
		{"not submitted", KYCStatus{Status: KycNotSubmitted}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.status.Done(); got != tc.want {
				t.Errorf("Done() = %v, want %v", got, tc.want)
			}
		})
	}
}

// The full PAN is PII: the status projection must never carry it, even though
// the record it is built from does.
func TestNewKYCStatus_DoesNotLeakFullPAN(t *testing.T) {
	const pan = "ABCDE1234F"
	b, err := json.Marshal(NewKYCStatus(&KYCRecord{PANNumber: pan, Status: KycPending}))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if got := string(b); strings.Contains(got, pan) {
		t.Errorf("serialized status contains the full PAN: %s", got)
	}
}

// Profile embeds Account, so the JSON must stay flat for existing clients and
// merely gain a "kyc" object.
func TestProfile_JSONShape(t *testing.T) {
	p := Profile{
		Account: Account{ID: 7, Status: AccountActive, Role: RoleUser},
		KYC:     NewKYCStatus(nil),
	}
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// Account fields stay at the top level, not nested under "account".
	if _, ok := got["account"]; ok {
		t.Errorf("account fields were nested instead of flattened: %s", b)
	}
	if id, _ := got["id"].(float64); id != 7 {
		t.Errorf("id = %v, want 7", got["id"])
	}
	kyc, ok := got["kyc"].(map[string]any)
	if !ok {
		t.Fatalf(`"kyc" missing or not an object: %s`, b)
	}
	if kyc["status"] != KycNotSubmitted {
		t.Errorf("kyc.status = %v, want %q", kyc["status"], KycNotSubmitted)
	}
}
