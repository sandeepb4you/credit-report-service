// Scheduled score checks (myScorr Plus): the prepaid-runs lifecycle, end to
// end against real handlers and a real Postgres — purchase webhook → batch
// minted → first run due today → early manual spend needs confirmation and
// re-anchors the schedule → the runner executes what is due and never burns
// quota on a failure.
//
// The seams are the same as the rest of this suite: the Digitap credit client
// is the offline stub (every pull "succeeds"), the payment gateway is the stub
// (every webhook signature verifies), and the master OTP signs accounts in.
package integration

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"credit-report-service/internal/config"
	"credit-report-service/internal/digitap"
	"credit-report-service/internal/repository"
	"credit-report-service/internal/service"
)

// buyPlan creates an order for productCode and settles it with a fabricated
// success webhook, returning the order UID. This is the REAL fulfilment path —
// ProcessWebhook → MarkOrderPaid → fulfillOrder → mint — not a DB shortcut.
func (h *harness) buyPlan(token, productCode string) string {
	h.t.Helper()

	created := h.post("/api/orders/", token, map[string]string{"productCode": productCode})
	if created.Status != http.StatusCreated {
		h.t.Fatalf("create %s order: %d %s", productCode, created.Status, created.Raw)
	}
	orderUID, _ := created.Body["orderId"].(string)
	if orderUID == "" {
		h.t.Fatalf("no orderId in create response: %s", created.Raw)
	}

	payload := fmt.Sprintf(`{
		"type": "PAYMENT_SUCCESS_WEBHOOK",
		"data": {
			"order": {"order_id": %q},
			"payment": {"cf_payment_id": "stub-pay-1", "payment_status": "SUCCESS",
			            "payment_group": "upi", "payment_time": %q}
		}
	}`, orderUID, time.Now().UTC().Format(time.RFC3339))

	req := httptest.NewRequest(http.MethodPost, "/api/payments/cashfree/webhook",
		strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-webhook-timestamp", "1")
	req.Header.Set("x-webhook-signature", "stub")
	req.Header.Set("x-idempotency-key", "test-"+orderUID)
	res, err := h.app.Test(req, -1)
	if err != nil {
		h.t.Fatalf("webhook for %s: %v", orderUID, err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		h.t.Fatalf("webhook for %s: status %d", orderUID, res.StatusCode)
	}
	return orderUID
}

// scheduledChecks reads the caller's schedule view.
func (h *harness) scheduledChecks(token string) map[string]any {
	h.t.Helper()
	res := h.get("/api/credit-analytics/scheduled-checks", token)
	if res.Status != http.StatusOK {
		h.t.Fatalf("scheduled-checks: %d %s", res.Status, res.Raw)
	}
	return res.Body
}

func pendingRuns(view map[string]any) int {
	n, _ := view["pendingRuns"].(float64)
	return int(n)
}

// planAccount signs a fresh phone account in and walks it through PAN
// verification (the stub confirms JOHN DOE/ABCDE1234F for any number), which
// is what fills the profile name the bureau payload needs.
func (h *harness) planAccount(phone string) (token string, accountID int64) {
	h.t.Helper()
	token, accountID = h.signInByPhone(phone, "")
	if res := h.verifyPAN(token, stubPAN, stubPANName); res.Status != http.StatusCreated {
		h.t.Fatalf("verify PAN: %d %s", res.Status, res.Raw)
	}
	return token, accountID
}

// today mirrors the service's business date (harness config has no timezone,
// so the runner and these assertions share the IST fallback).
func today() time.Time {
	loc := config.ScheduledChecksConfig{}.Location()
	y, m, d := time.Now().In(loc).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

func dateOf(v any) string {
	s, _ := v.(string)
	return s
}

func TestPlanPurchase_MintsTheFullScheduleWithTheFirstRunDueToday(t *testing.T) {
	h := newHarness(t)
	token, _ := h.planAccount("+919600000001")

	h.buyPlan(token, "SCORE_PLUS_QUARTERLY")

	view := h.scheduledChecks(token)
	if got := pendingRuns(view); got != 4 {
		t.Fatalf("pendingRuns = %d, want 4", got)
	}
	wantFirst := today().Format("2006-01-02")
	if got := dateOf(view["nextDueOn"]); got != wantFirst {
		t.Fatalf("nextDueOn = %q, want today %q", got, wantFirst)
	}
	if dateOf(view["planCode"]) != "SCORE_PLUS_QUARTERLY" {
		t.Fatalf("planCode = %v", view["planCode"])
	}
	runs, _ := view["runs"].([]any)
	if len(runs) != 4 {
		t.Fatalf("runs = %d, want 4", len(runs))
	}
	// The whole year is materialized up front, one row per quarter.
	for i, r := range runs {
		row, _ := r.(map[string]any)
		wantDue := today().AddDate(0, 3*i, 0).Format("2006-01-02")
		gotDue, _ := row["dueOn"].(string)
		if !strings.HasPrefix(gotDue, wantDue) {
			t.Fatalf("run %d dueOn = %q, want %q", i+1, gotDue, wantDue)
		}
	}
}

func TestFirstCheckAfterPurchase_RunsWithoutConfirmation(t *testing.T) {
	h := newHarness(t)
	token, _ := h.planAccount("+919600000002")
	h.buyPlan(token, "SCORE_PLUS_MONTHLY")

	// No use_scheduled_quota flag: the first run is DUE today, so nothing is
	// being taken early and no confirmation is demanded.
	res := h.post("/api/credit-analytics/request", token, map[string]any{})
	if res.Status != http.StatusCreated {
		t.Fatalf("first check: %d %s", res.Status, res.Raw)
	}

	view := h.scheduledChecks(token)
	if got := pendingRuns(view); got != 11 {
		t.Fatalf("pendingRuns after first check = %d, want 11", got)
	}
	// Spending re-anchors from the spend day; for an on-schedule spend that is
	// the same cadence: next due one month from today.
	want := today().AddDate(0, 1, 0).Format("2006-01-02")
	if got := dateOf(view["nextDueOn"]); got != want {
		t.Fatalf("nextDueOn = %q, want %q", got, want)
	}
}

func TestEarlyManualCheck_Needs409ConfirmationAndThenReanchors(t *testing.T) {
	h := newHarness(t)
	token, _ := h.planAccount("+919600000003")
	h.buyPlan(token, "SCORE_PLUS_MONTHLY")

	// Consume the due first run so the next pending sits a month out.
	if res := h.post("/api/credit-analytics/request", token, map[string]any{}); res.Status != http.StatusCreated {
		t.Fatalf("first check: %d %s", res.Status, res.Raw)
	}

	// An early on-demand check without the flag is refused with the dialog's
	// numbers, and spends nothing.
	refused := h.post("/api/credit-analytics/request", token, map[string]any{})
	if refused.Status != http.StatusConflict {
		t.Fatalf("early check without flag: %d %s, want 409", refused.Status, refused.Raw)
	}
	details, _ := refused.Body["details"].(map[string]any)
	if dateOf(details["reason"]) != "scheduled_quota_confirm" {
		t.Fatalf("409 details = %v", refused.Body["details"])
	}
	if dateOf(details["nextDueOn"]) != today().AddDate(0, 1, 0).Format("2006-01-02") {
		t.Fatalf("409 nextDueOn = %v", details["nextDueOn"])
	}
	if got := pendingRuns(h.scheduledChecks(token)); got != 11 {
		t.Fatalf("pendingRuns after refusal = %d, want 11 (nothing spent)", got)
	}

	// Confirmed: the run is spent and the remaining schedule restarts from today.
	confirmed := h.post("/api/credit-analytics/request", token,
		map[string]any{"use_scheduled_quota": true})
	if confirmed.Status != http.StatusCreated {
		t.Fatalf("confirmed early check: %d %s", confirmed.Status, confirmed.Raw)
	}
	view := h.scheduledChecks(token)
	if got := pendingRuns(view); got != 10 {
		t.Fatalf("pendingRuns after confirmed spend = %d, want 10", got)
	}
	want := today().AddDate(0, 1, 0).Format("2006-01-02")
	if got := dateOf(view["nextDueOn"]); got != want {
		t.Fatalf("nextDueOn after re-anchor = %q, want %q", got, want)
	}
}

func TestOneTimePurchase_IsSpentBeforeScheduledQuotaAndLeavesTheScheduleAlone(t *testing.T) {
	h := newHarness(t)
	token, _ := h.planAccount("+919600000004")
	h.buyPlan(token, "SCORE_PLUS_MONTHLY")
	if res := h.post("/api/credit-analytics/request", token, map[string]any{}); res.Status != http.StatusCreated {
		t.Fatalf("first check: %d %s", res.Status, res.Raw)
	}
	before := h.scheduledChecks(token)

	// A Starter check bought alongside the plan funds the next pull silently —
	// no 409, no schedule movement, no flag needed.
	h.buyPlan(token, "CREDIT_ANALYSIS")
	res := h.post("/api/credit-analytics/request", token, map[string]any{})
	if res.Status != http.StatusCreated {
		t.Fatalf("one-time-funded check: %d %s", res.Status, res.Raw)
	}

	after := h.scheduledChecks(token)
	if pendingRuns(after) != pendingRuns(before) {
		t.Fatalf("pendingRuns moved %d -> %d; the one-time order should have paid",
			pendingRuns(before), pendingRuns(after))
	}
	if dateOf(after["nextDueOn"]) != dateOf(before["nextDueOn"]) {
		t.Fatalf("nextDueOn moved %v -> %v; one-time spends must not re-anchor",
			before["nextDueOn"], after["nextDueOn"])
	}
}

func TestSecondPlanPurchase_IsRefusedWhileRunsArePending(t *testing.T) {
	h := newHarness(t)
	token, _ := h.planAccount("+919600000005")
	h.buyPlan(token, "SCORE_PLUS_QUARTERLY")

	res := h.post("/api/orders/", token, map[string]string{"productCode": "SCORE_PLUS_MONTHLY"})
	if res.Status != http.StatusConflict {
		t.Fatalf("second plan purchase: %d %s, want 409", res.Status, res.Raw)
	}
	// One-time checks stay purchasable alongside a plan.
	if res := h.post("/api/orders/", token, map[string]string{"productCode": "CREDIT_ANALYSIS"}); res.Status != http.StatusCreated {
		t.Fatalf("one-time purchase alongside plan: %d %s", res.Status, res.Raw)
	}
}

// runner builds a ScheduledCheckRunner over the harness's pool, wired exactly
// like main.go but with the same stub seams as buildApp.
func (h *harness) runner() *service.ScheduledCheckRunner {
	h.t.Helper()
	scheduledRepo := repository.NewScheduledCheckRepo(h.pool)
	cfg := config.ScheduledChecksConfig{
		PollInterval: time.Hour, MaxAttempts: 3, StaleAfter: 15 * time.Minute, BatchSize: 25,
	}
	analytics := service.NewCreditAnalyticsService(
		digitap.New(digitap.Config{}),
		repository.NewCreditAnalyticsRepo(h.pool),
		h.accts,
		repository.NewOrderRepo(h.pool),
		scheduledRepo,
		config.CreditAnalyticsConfig{},
		cfg.Location(),
	)
	return service.NewScheduledCheckRunner(scheduledRepo, analytics, cfg)
}

func TestRunnerSweep_ExecutesTheDueRunAndCompletesIt(t *testing.T) {
	h := newHarness(t)
	token, accountID := h.planAccount("+919600000006")
	h.buyPlan(token, "SCORE_PLUS_QUARTERLY")

	h.runner().Sweep(context.Background())

	view := h.scheduledChecks(token)
	if got := pendingRuns(view); got != 3 {
		t.Fatalf("pendingRuns after sweep = %d, want 3", got)
	}
	var status, by string
	var reportID *int64
	if err := h.pool.QueryRow(h.baseCtx,
		`SELECT status, completed_by, report_id FROM scheduled_score_checks
		 WHERE account_id = $1 AND sequence_no = 1`, accountID,
	).Scan(&status, &by, &reportID); err != nil {
		t.Fatalf("read run 1: %v", err)
	}
	if status != "DONE" || by != "runner" || reportID == nil {
		t.Fatalf("run 1 = %s/%s/report %v, want DONE/runner/non-nil", status, by, reportID)
	}
	// A second sweep finds nothing due — the period ran once and cannot re-run.
	h.runner().Sweep(context.Background())
	if got := pendingRuns(h.scheduledChecks(token)); got != 3 {
		t.Fatalf("second sweep changed pendingRuns to %d", got)
	}
}

func TestRunnerSweep_FailedRunStaysPendingAndBurnsNoQuota(t *testing.T) {
	h := newHarness(t)
	// No PAN verification: buildPayload refuses the pull, so the run FAILS.
	token, accountID := h.signInByPhone("+919600000007", "")
	h.buyPlan(token, "SCORE_PLUS_QUARTERLY")

	h.runner().Sweep(context.Background())

	var status string
	var attempts int
	if err := h.pool.QueryRow(h.baseCtx,
		`SELECT status, attempts FROM scheduled_score_checks
		 WHERE account_id = $1 AND sequence_no = 1`, accountID,
	).Scan(&status, &attempts); err != nil {
		t.Fatalf("read run 1: %v", err)
	}
	if status != "PENDING" || attempts != 1 {
		t.Fatalf("failed run = %s/attempts %d, want PENDING/1 (retryable, quota intact)", status, attempts)
	}
	if got := pendingRuns(h.scheduledChecks(token)); got != 4 {
		t.Fatalf("pendingRuns = %d, want 4 — a failure must not spend a run", got)
	}
}

func TestRunnerSweep_ReclaimsACrashedClaimAndExpiresTheOverdue(t *testing.T) {
	h := newHarness(t)
	token, accountID := h.planAccount("+919600000008")
	h.buyPlan(token, "SCORE_PLUS_QUARTERLY")

	// Simulate a crash: run 1 claimed long ago and never settled; run 4's plan
	// year is over.
	if _, err := h.pool.Exec(h.baseCtx,
		`UPDATE scheduled_score_checks SET status = 'RUNNING',
		     last_attempt_at = now() - interval '2 hours'
		 WHERE account_id = $1 AND sequence_no = 1`, accountID); err != nil {
		t.Fatalf("orphan run 1: %v", err)
	}
	if _, err := h.pool.Exec(h.baseCtx,
		`UPDATE scheduled_score_checks SET expires_on = current_date - 1
		 WHERE account_id = $1 AND sequence_no = 4`, accountID); err != nil {
		t.Fatalf("expire run 4: %v", err)
	}

	h.runner().Sweep(context.Background())

	rows := map[int]string{}
	r, err := h.pool.Query(h.baseCtx,
		`SELECT sequence_no, status FROM scheduled_score_checks WHERE account_id = $1`, accountID)
	if err != nil {
		t.Fatalf("read runs: %v", err)
	}
	defer r.Close()
	for r.Next() {
		var seq int
		var st string
		if err := r.Scan(&seq, &st); err != nil {
			t.Fatalf("scan: %v", err)
		}
		rows[seq] = st
	}
	// The orphaned claim was reclaimed and, being due, executed in the same
	// sweep; the overdue run was voided; the mid-plan runs are untouched.
	if rows[1] != "DONE" {
		t.Fatalf("run 1 = %s, want DONE (reclaim + execute)", rows[1])
	}
	if rows[4] != "EXPIRED" {
		t.Fatalf("run 4 = %s, want EXPIRED", rows[4])
	}
	if rows[2] != "PENDING" || rows[3] != "PENDING" {
		t.Fatalf("mid-plan runs = %s/%s, want PENDING/PENDING", rows[2], rows[3])
	}
	_ = token
}
