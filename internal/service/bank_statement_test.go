package service

import (
	"context"
	"testing"
	"time"
)

// TestParseTransactions_TwoColumn is the canonical HDFC/SBI layout: a leading
// date and two trailing money columns (debit, credit). The salary row's amount
// must land in the credit column; the EMI row in the debit column.
func TestParseTransactions_TwoColumn(t *testing.T) {
	text := `Account Statement
05/04/2024 INFOSYS LTD SALARY                0.00      85000.00
10/04/2024 NEFT/HOMELOAN EMI/NACH            18500.00  0.00
12/04/2024 VISA/CARD SWIPE/AMAZON            3500.00   0.00
`
	txns, warnings := parseTransactions(text)
	if len(txns) != 3 {
		t.Fatalf("expected 3 transactions, got %d: %+v", len(txns), txns)
	}
	if len(warnings) != 0 {
		t.Fatalf("expected no warnings, got %v", warnings)
	}

	salary := txns[0]
	if salary.Direction != directionCredit || salary.Amount != 85000.00 {
		t.Errorf("salary row = %s %.2f, want credit 85000.00", salary.Direction, salary.Amount)
	}
	if salary.Description == "" {
		t.Errorf("salary description empty")
	}

	emi := txns[1]
	if emi.Direction != directionDebit || emi.Amount != 18500.00 {
		t.Errorf("emi row = %s %.2f, want debit 18500.00", emi.Direction, emi.Amount)
	}
}

// TestParseTransactions_CRDRSuffix covers statements that mark direction with a
// trailing CR/DR instead of using two amount columns (common in ICICI exports).
func TestParseTransactions_CRDRSuffix(t *testing.T) {
	text := `05/04/2024 INFOSYS LTD SALARY            85000.00 CR
06/04/2024 UPI/To GROCERY STORE              1200.50 DR
`
	txns, _ := parseTransactions(text)
	if len(txns) != 2 {
		t.Fatalf("expected 2 transactions, got %d", len(txns))
	}
	if txns[0].Direction != directionCredit || txns[0].Amount != 85000.00 {
		t.Errorf("credit row = %s %.2f, want credit 85000.00", txns[0].Direction, txns[0].Amount)
	}
	if txns[1].Direction != directionDebit || txns[1].Amount != 1200.50 {
		t.Errorf("debit row = %s %.2f, want debit 1200.50", txns[1].Direction, txns[1].Amount)
	}
}

// TestParseTransactions_TextMonth covers the "05 Apr 2024" date format and
// Indian-style amounts with thousands separators.
func TestParseTransactions_TextMonth(t *testing.T) {
	text := `05 Apr 2024 INFOSYS LTD SALARY         1,85,000.00 CR
10 Apr 2024 HOMELOAN EMI NACH              18,500.00 DR
`
	txns, _ := parseTransactions(text)
	if len(txns) != 2 {
		t.Fatalf("expected 2 transactions, got %d", len(txns))
	}
	if txns[0].Amount != 185000.00 {
		t.Errorf("lakh amount = %.2f, want 185000.00", txns[0].Amount)
	}
	if !txns[0].Date.Equal(time.Date(2024, time.April, 5, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("date = %v, want 2024-04-05", txns[0].Date)
	}
}

// TestParseTransactions_SkipsNonTransactionLines ensures headers, blanks, and
// footer totals don't get turned into bogus transactions.
func TestParseTransactions_SkipsNonTransactionLines(t *testing.T) {
	text := `Account Statement
Account No: 50100123456789
From 01/04/2024 to 31/05/2024

Date       Narration                  Withdrawal     Deposit

05/04/2024 INFOSYS LTD SALARY          0.00      85000.00
Closing Balance: 250000.00
`
	txns, warnings := parseTransactions(text)
	if len(txns) != 1 {
		t.Fatalf("expected 1 transaction, got %d: %+v", len(txns), txns)
	}
	// The "Closing Balance" line has digits but no leading date, so it's a skip
	// (warned) rather than a transaction.
	if len(warnings) == 0 {
		t.Errorf("expected a parse warning for the unparseable balance line")
	}
}

// TestAnalyze_SalaryDetection_Keyword checks the explicit SALARY keyword path
// and that the headline totals roll up correctly.
func TestAnalyze_SalaryDetection_Keyword(t *testing.T) {
	text := `05/04/2024 INFOSYS LTD SALARY          0.00      85000.00
05/05/2024 INFOSYS LTD SALARY          0.00      86000.00
10/04/2024 HOMELOAN EMI NACH            18500.00  0.00
06/04/2024 UPI/To GROCERY STORE         1200.00   0.00
`
	a := analyze(text)
	if a.Salary == nil {
		t.Fatalf("expected salary detected, got nil")
	}
	if a.Salary.Occurrences < 2 {
		t.Errorf("salary occurrences = %d, want >= 2", a.Salary.Occurrences)
	}
	if a.Salary.Direction != directionCredit {
		t.Errorf("salary direction = %s, want credit", a.Salary.Direction)
	}
	if a.Summary.TotalCredits != 171000.00 {
		t.Errorf("total credits = %.2f, want 171000.00", a.Summary.TotalCredits)
	}
	if a.Summary.TotalDebits != 19700.00 {
		t.Errorf("total debits = %.2f, want 19700.00", a.Summary.TotalDebits)
	}
}

// TestAnalyze_SalaryDetection_RecurringNoKeyword verifies that a recurring
// monthly credit is flagged as salary even without the SALARY keyword, as long
// as it clears the threshold. A single one-off credit must NOT be flagged.
func TestAnalyze_SalaryDetection_RecurringNoKeyword(t *testing.T) {
	recurring := `05/04/2024 ABC CORP TRANSFER          0.00     60000.00
05/05/2024 ABC CORP TRANSFER          0.00     60000.00
`
	oneOff := `05/04/2024 FRIEND REFUND                0.00     60000.00
`
	if a := analyze(recurring); a.Salary == nil {
		t.Errorf("recurring credit should be detected as salary")
	}
	if a := analyze(oneOff); a.Salary != nil {
		t.Errorf("a single one-off credit must not be flagged as salary")
	}
}

// TestAnalyze_EMI ensures keyword and recurring EMI detection both fire and the
// debit direction is preserved.
func TestAnalyze_EMI(t *testing.T) {
	text := `10/04/2024 HOMELOAN EMI NACH            18500.00  0.00
10/05/2024 HOMELOAN EMI NACH            18500.00  0.00
15/04/2024 CAR LOAN INSTALMENT          8500.00   0.00
`
	a := analyze(text)
	if len(a.EMIs) < 2 {
		t.Fatalf("expected >= 2 EMIs, got %d: %+v", len(a.EMIs), a.EMIs)
	}
	for _, emi := range a.EMIs {
		if emi.Direction != directionDebit {
			t.Errorf("EMI direction = %s, want debit", emi.Direction)
		}
		if emi.Amount <= 0 {
			t.Errorf("EMI amount = %.2f, want positive", emi.Amount)
		}
	}
}

// TestAnalyze_Categories verifies the payment-rail bucketing.
func TestAnalyze_Categories(t *testing.T) {
	text := `06/04/2024 UPI/To GROCERY STORE         1200.00   0.00
07/04/2024 ATM/WITHDRAWAL              2000.00   0.00
12/04/2024 VISA/CARD SWIPE/AMAZON      3500.00   0.00
08/04/2024 NEFT/To LANDLORD            15000.00  0.00
`
	a := analyze(text)
	got := map[string]CategoryTotal{}
	for _, c := range a.Categories {
		got[c.Category] = c
	}
	for _, want := range []string{"upi", "atm", "card", "neft_imps_rtgs"} {
		if _, ok := got[want]; !ok {
			t.Errorf("missing category %q in %+v", want, got)
		}
	}
	if got["upi"].Total != 1200.00 {
		t.Errorf("upi total = %.2f, want 1200.00", got["upi"].Total)
	}
}

// TestAnalyze_Subscriptions checks the known-service detection.
func TestAnalyze_Subscriptions(t *testing.T) {
	text := `28/04/2024 NETFLIX SUBSCRIPTION         649.00   0.00
28/05/2024 NETFLIX SUBSCRIPTION         649.00   0.00
01/05/2024 SPOTIFY PREMIUM              119.00   0.00
`
	a := analyze(text)
	if len(a.Subscriptions) == 0 {
		t.Fatalf("expected subscriptions detected, got none")
	}
}

// TestAnalyze_MonthlyTotals verifies per-month aggregation and that the month
// keys sort ascending.
func TestAnalyze_MonthlyTotals(t *testing.T) {
	text := `05/04/2024 SALARY                      0.00      50000.00
10/04/2024 RENT                         15000.00  0.00
05/05/2024 SALARY                      0.00      50000.00
`
	a := analyze(text)
	if len(a.MonthlyTotals) != 2 {
		t.Fatalf("expected 2 monthly buckets, got %d", len(a.MonthlyTotals))
	}
	if a.MonthlyTotals[0].Month != "2024-04" {
		t.Errorf("first month = %s, want 2024-04", a.MonthlyTotals[0].Month)
	}
	if a.MonthlyTotals[1].Credit != 50000.00 {
		t.Errorf("May credit = %.2f, want 50000.00", a.MonthlyTotals[1].Credit)
	}
}

// TestAnalyze_EmptyText guarantees a scanned/empty PDF degrades gracefully: no
// panic, no transactions, and the analysis is still well-formed.
func TestAnalyze_EmptyText(t *testing.T) {
	a := analyze("")
	if len(a.Transactions) != 0 {
		t.Errorf("expected 0 transactions, got %d", len(a.Transactions))
	}
	if a.Summary.TransactionCount != 0 {
		t.Errorf("expected summary count 0, got %d", a.Summary.TransactionCount)
	}
}

// TestDetectRecurring_SingleOccurrenceNotRecurring confirms one transaction in
// a single month is never treated as recurring (needs >= 2 distinct months).
func TestDetectRecurring_SingleOccurrenceNotRecurring(t *testing.T) {
	txns := []Transaction{
		{Date: time.Date(2024, 4, 10, 0, 0, 0, 0, time.UTC), Description: "EMI", Amount: 1000, Direction: directionDebit},
	}
	if isMonthlyRecurring(txns) {
		t.Errorf("a single transaction must not count as recurring")
	}
}

// TestMonthsBetween covers the averaging helper.
func TestMonthsBetween(t *testing.T) {
	cases := []struct {
		a, b time.Time
		want int
	}{
		{time.Date(2024, 4, 1, 0, 0, 0, 0, time.UTC), time.Date(2024, 4, 30, 0, 0, 0, 0, time.UTC), 1},
		{time.Date(2024, 4, 1, 0, 0, 0, 0, time.UTC), time.Date(2024, 5, 31, 0, 0, 0, 0, time.UTC), 2},
		{time.Date(2024, 4, 1, 0, 0, 0, 0, time.UTC), time.Date(2024, 6, 15, 0, 0, 0, 0, time.UTC), 3},
	}
	for _, c := range cases {
		if got := monthsBetween(c.a, c.b); got != c.want {
			t.Errorf("monthsBetween(%v, %v) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

// ---- Worker pool ----------------------------------------------------------

// fakeProcessor records processed jobs so tests can assert the pool drains.
type fakeProcessor struct {
	jobs []Job
}

func (f *fakeProcessor) process(_ context.Context, job Job) {
	f.jobs = append(f.jobs, job)
}

// TestWorkerPool_RunsJobs confirms a submitted job is processed and Stop drains.
func TestWorkerPool_RunsJobs(t *testing.T) {
	proc := &fakeProcessor{}
	pool := NewWorkerPool(proc, 2, 4, time.Minute)
	pool.Start(context.Background())

	if err := pool.Submit(Job{StatementID: 1}); err != nil {
		t.Fatalf("Submit error: %v", err)
	}
	pool.Stop()

	// After Stop, the one submitted job should have been processed.
	if len(proc.jobs) != 1 {
		t.Errorf("expected 1 processed job, got %d", len(proc.jobs))
	}
}

// TestWorkerPool_QueueFull confirms Submit returns ErrQueueFull once the buffer
// is saturated, rather than blocking.
func TestWorkerPool_QueueFull(t *testing.T) {
	proc := &fakeProcessor{}
	// buffer 1, no workers started — the first Submit fills the buffer, the
	// second must return ErrQueueFull immediately.
	pool := NewWorkerPool(proc, 1, 1, time.Minute)
	if err := pool.Submit(Job{StatementID: 1}); err != nil {
		t.Fatalf("first Submit error: %v", err)
	}
	if err := pool.Submit(Job{StatementID: 2}); err != ErrQueueFull {
		t.Errorf("second Submit error = %v, want ErrQueueFull", err)
	}
}
