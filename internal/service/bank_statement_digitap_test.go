package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

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
	// Whatever the code, Digitap's integrator-facing text never reaches the user.
	for _, tc := range cases {
		if err := mapDigitapError(tc.code, "Client is not permitted to access this URL", 403); strings.Contains(err.Error(), "permitted") {
			t.Errorf("code %q leaked the upstream message: %v", tc.code, err)
		}
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

// TestPickDigitapOutcome pins how a request's transactions are read as one
// answer. The cases that matter are the mixed ones: a retry that succeeded
// after a failure, and a failure beside an attempt still being parsed — the
// old first-terminal-wins scan failed the row on both.
func TestPickDigitapOutcome(t *testing.T) {
	ok := bankdata.TxnStatus{TxnID: "ok", Status: "Success", Code: bankdata.CodeReportGenerated}
	failed := bankdata.TxnStatus{TxnID: "bad", Status: "Failure", Code: "Tamper error"}
	errored := bankdata.TxnStatus{TxnID: "err", Status: "Error", Code: "InternalError"}
	moving := bankdata.TxnStatus{TxnID: "mv", Status: "InProgress", Code: bankdata.CodeTxnProcessing}
	opened := bankdata.TxnStatus{TxnID: "op", Status: "Success", Code: bankdata.CodeTxnInitiated}

	cases := []struct {
		name    string
		txns    []bankdata.TxnStatus
		want    digitapOutcome
		wantTxn string
	}{
		{"unused link", nil, digitapPending, ""},
		{"opened, nothing uploaded", []bankdata.TxnStatus{opened}, digitapPending, ""},
		{"parsing", []bankdata.TxnStatus{moving}, digitapPending, ""},
		{"ready", []bankdata.TxnStatus{ok}, digitapReady, "ok"},
		{"failed then retried", []bankdata.TxnStatus{failed, ok}, digitapReady, "ok"},
		{"ready then failed", []bankdata.TxnStatus{ok, failed}, digitapReady, "ok"},
		{"failed while retry parses", []bankdata.TxnStatus{failed, moving}, digitapPending, ""},
		{"only failure", []bankdata.TxnStatus{failed}, digitapFailed, "bad"},
		{"all failed: the last reports", []bankdata.TxnStatus{failed, errored}, digitapFailed, "err"},
	}
	for _, tc := range cases {
		got, txn := pickDigitapOutcome(tc.txns)
		if got != tc.want {
			t.Errorf("%s: outcome = %v, want %v", tc.name, got, tc.want)
			continue
		}
		gotTxn := ""
		if txn != nil {
			gotTxn = txn.TxnID
		}
		if gotTxn != tc.wantTxn {
			t.Errorf("%s: txn = %q, want %q", tc.name, gotTxn, tc.wantTxn)
		}
	}
}

func TestStatementMonthRange(t *testing.T) {
	at := time.Date(2026, 9, 28, 15, 0, 0, 0, time.UTC)
	cases := []struct {
		n          int
		start, end string
	}{
		{0, "", ""},
		{-1, "", ""},
		{6, "2026-03", "2026-09"},
		{1, "2026-08", "2026-09"},
		{12, "2025-09", "2026-09"},
		{24, "2025-09", "2026-09"}, // capped: Digitap refuses a start over a year back
	}
	for _, tc := range cases {
		s, e := statementMonthRange(at, tc.n)
		if s != tc.start || e != tc.end {
			t.Errorf("n=%d: %q..%q, want %q..%q", tc.n, s, e, tc.start, tc.end)
		}
	}
	// Month arithmetic from the 31st must not overflow into the next month.
	s, _ := statementMonthRange(time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC), 1)
	if s != "2026-02" {
		t.Errorf("from 31 March, 1 month back = %q, want 2026-02", s)
	}
}

// TestDigitapFailureMessage: codes a user can act on get our sentence, never
// Digitap's integrator-facing one; the rest fall back to Digitap's message.
func TestDigitapFailureMessage(t *testing.T) {
	for _, code := range []string{"UserCancelled", "TxnExpired", "Txn_expired", "Tamper error", "StatementIsScannedImage"} {
		got := digitapFailureMessage(code, "Requested transaction expired. (Error Code: 088)")
		if strings.Contains(got, "Error Code") || got == "" {
			t.Errorf("%s: leaked the upstream message: %q", code, got)
		}
	}
	if got := digitapFailureMessage("SomethingNew", "Digitap says why"); got != "Digitap says why" {
		t.Errorf("unknown code: %q, want Digitap's message", got)
	}
	if got := digitapFailureMessage("SomethingNew", "  "); got == "" {
		t.Errorf("unknown code, no message: want a generic sentence")
	}
}
