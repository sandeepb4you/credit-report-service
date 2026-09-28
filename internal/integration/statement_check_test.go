package integration

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"credit-report-service/internal/repository"
)

// TestStatementCheck_DigitapFlow walks Statement Check the way the app does:
// start an upload, poll the row, read the stored report. Against the offline
// bank-data stub, which reports every upload as generated, so this pins our
// half — routing, ownership, persistence and the cap — not Digitap's.
func TestStatementCheck_DigitapFlow(t *testing.T) {
	h := newHarness(t)
	token, _ := h.signInByPhone("+919000000801", "")

	start := h.post("/api/bank-statements/digitap/initiate", token, map[string]any{"embedded": true})
	if start.Status != http.StatusOK {
		t.Fatalf("initiate: %d %s", start.Status, start.Raw)
	}
	id := intOf(start.Body["id"])
	if id == 0 || start.Body["redirectUrl"] == "" {
		t.Fatalf("initiate returned no row or url: %s", start.Raw)
	}

	// The stub's zone-less expiry must land in the column, not be dropped.
	var expires *time.Time
	if err := h.pool.QueryRow(h.baseCtx,
		`SELECT url_expires_at FROM bank_statements WHERE id = $1`, id).Scan(&expires); err != nil {
		t.Fatalf("read expiry: %v", err)
	}
	if expires == nil {
		t.Errorf("url_expires_at is NULL: the doc's timestamp shape did not parse")
	}

	// First poll runs the status-check fallback and completes the row.
	row := h.get(fmt.Sprintf("/api/bank-statements/%d", id), token)
	if row.Status != http.StatusOK {
		t.Fatalf("poll: %d %s", row.Status, row.Raw)
	}
	if row.Body["status"] != "completed" || row.Body["provider"] != "digitap" {
		t.Fatalf("poll: status=%v provider=%v, want completed/digitap", row.Body["status"], row.Body["provider"])
	}
	if _, ok := row.Body["analysis"].(map[string]any); !ok {
		t.Errorf("completed row carries no report: %s", row.Raw)
	}
	if row.Body["txnId"] == "" || row.Body["txnId"] == nil {
		t.Errorf("txn_id not recorded")
	}

	// The list shows it, with the provider the app keys its rendering on.
	list := h.get("/api/bank-statements?size=1", token)
	items := listOf(list.Body["items"])
	if len(items) != 1 || intOf(items[0]["id"]) != id || items[0]["provider"] != "digitap" {
		t.Errorf("list: %s", list.Raw)
	}

	// Someone else's row is a 404, not a read.
	other, _ := h.signInByPhone("+919000000802", "")
	if res := h.get(fmt.Sprintf("/api/bank-statements/%d", id), other); res.Status != http.StatusNotFound {
		t.Errorf("another account read the row: %d", res.Status)
	}

	// A callback naming no known request is acknowledged and changes nothing.
	cb := h.do(http.MethodPost, "/api/bank-statements/digitap/callback", "", map[string]any{
		"request_id": "nobody", "txn_id": "x", "status": "Failure",
	})
	if cb.Status != http.StatusOK {
		t.Errorf("unknown callback: %d %s", cb.Status, cb.Raw)
	}
}

// Every started upload is a billed Digitap transaction, so an account gets
// testStatementDailyLimit a day and then a 429 — and another account is
// unaffected by the first one's count.
func TestStatementCheck_DailyLimit(t *testing.T) {
	h := newHarness(t)
	token, _ := h.signInByPhone("+919000000811", "")

	for i := 0; i < testStatementDailyLimit; i++ {
		if res := h.post("/api/bank-statements/digitap/initiate", token, nil); res.Status != http.StatusOK {
			t.Fatalf("initiate %d: %d %s", i+1, res.Status, res.Raw)
		}
	}
	res := h.post("/api/bank-statements/digitap/initiate", token, nil)
	if res.Status != http.StatusTooManyRequests {
		t.Fatalf("over the cap: %d %s, want 429", res.Status, res.Raw)
	}

	other, _ := h.signInByPhone("+919000000812", "")
	if res := h.post("/api/bank-statements/digitap/initiate", other, nil); res.Status != http.StatusOK {
		t.Errorf("a second account was capped by the first: %d", res.Status)
	}
}

// A restart must not fail a Digitap upload the user is still in the middle
// of; the boot sweep is for local parses this process owned.
func TestStatementCheck_RestartLeavesDigitapRows(t *testing.T) {
	h := newHarness(t)
	token, _ := h.signInByPhone("+919000000821", "")
	start := h.post("/api/bank-statements/digitap/initiate", token, nil)
	id := intOf(start.Body["id"])

	if _, err := h.pool.Exec(h.baseCtx,
		`UPDATE bank_statements SET created_at = now() - interval '1 hour' WHERE id = $1`, id); err != nil {
		t.Fatalf("backdate: %v", err)
	}
	repo := repository.NewBankStatementRepo(h.pool)
	if _, err := repo.ReclaimStaleProcessing(h.baseCtx, time.Now().Add(-5*time.Minute)); err != nil {
		t.Fatalf("reclaim: %v", err)
	}
	var status string
	if err := h.pool.QueryRow(h.baseCtx,
		`SELECT status FROM bank_statements WHERE id = $1`, id).Scan(&status); err != nil {
		t.Fatalf("read status: %v", err)
	}
	if status != "processing" {
		t.Errorf("a restart failed an open Digitap upload: status %q", status)
	}
}
