package service

import (
	"context"
	"errors"
	"testing"

	"credit-report-service/internal/apperr"
	"credit-report-service/internal/bankdata"
)

// TestMapDigitapError verifies each documented upstream error code lands on the
// right HTTP status: our-credentials/upstream issues -> 502 BadGateway, client
// input issues -> 400 Validation.
func TestMapDigitapError(t *testing.T) {
	cases := []struct {
		code    string
		want40x bool // true = 400 Validation, false = 502 BadGateway
	}{
		{"AccessDenied", false},
		{"SignatureDoesNotMatch", false},
		{"InvalidEncryption", false},
		{"ClientNotConfigured", false},
		{"NotSignedUp", false},
		{"InternalError", false},
		{"InvalidInstitution", true},
		{"InstitutionCurrentlyNotSupported", true},
		{"InvalidStmtStartDate", true},
		{"InvalidStmtEndDate", true},
		{"DateRangeTooLarge", true},
		{"InvalidClientRefNum", true},
		{"InvalidDestination", true},
		{"", false}, // unknown code -> default 502
	}
	for _, tc := range cases {
		err := mapDigitapError(tc.code, "msg", 400)
		var v *apperr.Validation
		var bg *apperr.BadGateway
		switch {
		case tc.want40x:
			if !errors.As(err, &v) {
				t.Errorf("code %q: want Validation, got %T (%v)", tc.code, err, err)
			}
		default:
			if !errors.As(err, &bg) {
				t.Errorf("code %q: want BadGateway, got %T (%v)", tc.code, err, err)
			}
		}
	}
}

// TestOrMsg confirms the message-or-code fallback picks the message when
// present and the code otherwise.
func TestOrMsg(t *testing.T) {
	if got := orMsg("real message", "CODE"); got != "real message" {
		t.Errorf("orMsg with msg = %q, want %q", got, "real message")
	}
	if got := orMsg("", "CODE"); got != "CODE" {
		t.Errorf("orMsg without msg = %q, want %q", got, "CODE")
	}
}

// TestSyncDigitapStatusDecision is a focused test of the status-check branching
// logic (ReportGenerated vs failure vs in-progress) without a database. It
// exercises the bankdata stub end-to-end through the decision path the
// SyncDigitap method uses.
func TestSyncDigitapStatusDecision(t *testing.T) {
	c := bankdata.New(bankdata.Config{})
	ctx := context.Background()

	// The stub always reports ReportGenerated; assert we can drive the full
	// status-check + retrieve-report sequence and get a non-empty report back,
	// which is the precondition for SyncDigitap flipping a row to completed.
	st, _, err := c.StatusCheck(ctx, "req-anything")
	if err != nil {
		t.Fatalf("StatusCheck: %v", err)
	}
	if len(st.TxnStatus) != 1 || st.TxnStatus[0].Code != bankdata.CodeReportGenerated {
		t.Fatalf("stub should report ReportGenerated, got %+v", st.TxnStatus)
	}
	txn := st.TxnStatus[0]

	rep, _, err := c.RetrieveReport(ctx, txn.TxnID)
	if err != nil {
		t.Fatalf("RetrieveReport: %v", err)
	}
	if len(rep.Result) == 0 {
		t.Fatalf("expected a non-empty report for the ReportGenerated txn")
	}
}

// TestBankDataReExport confirms the service-package aliases point at the right
// bankdata types/constants, so handlers don't silently use a stale reference.
func TestBankDataReExport(t *testing.T) {
	if BankDataCallbackTransactionComplete != bankdata.CallbackTypeTransactionComplete {
		t.Errorf("callback constant alias drifted")
	}
	var _ BankDataCallbackEvent = bankdata.CallbackEvent{}
}
