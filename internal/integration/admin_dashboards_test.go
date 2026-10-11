package integration

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"credit-report-service/internal/models"
)

func utcToday() string { return time.Now().UTC().Format("2006-01-02") }

func daysAgo(n int) string { return time.Now().UTC().AddDate(0, 0, -n).Format("2006-01-02") }

func (h *harness) series(token, key, from, to string) response {
	h.t.Helper()
	return h.get(fmt.Sprintf("/api/admin/dashboards/metrics/%s?from=%s&to=%s", key, from, to), token)
}

// The series counts what happened, by UTC day, gap-filled — and money counts
// live payments only, so the internal APK's test buys never reach revenue.
func TestDashboardSeriesCountsLivePaymentsAndFillsGaps(t *testing.T) {
	h := newHarness(t)
	admin := h.makeAdmin("+919000001001")

	buyer, _ := h.signInByPhone("+919000001002", "")
	liveUID := h.buyPlan(buyer, "CREDIT_ANALYSIS")
	h.buyPlan(buyer, "CREDIT_ANALYSIS") // stays sandbox: a test purchase
	if _, err := h.pool.Exec(h.baseCtx,
		`UPDATE orders SET payment_mode = 'production' WHERE order_uid = $1`, liveUID); err != nil {
		t.Fatalf("mark order live: %v", err)
	}
	var liveAmount float64
	if err := h.pool.QueryRow(h.baseCtx,
		`SELECT amount::float8 FROM orders WHERE order_uid = $1`, liveUID).Scan(&liveAmount); err != nil {
		t.Fatalf("read amount: %v", err)
	}

	from := daysAgo(6)
	signups := h.series(admin, "signups", from, utcToday())
	if signups.Status != http.StatusOK {
		t.Fatalf("signups: %d %s", signups.Status, signups.Raw)
	}
	if got := len(listOf(signups.Body["points"])); got != 7 {
		t.Errorf("7 days inclusive want 7 points, got %d: %s", got, signups.Raw)
	}
	if signups.Body["total"] != float64(2) {
		t.Errorf("signups total = %v, want the admin and the buyer", signups.Body["total"])
	}

	paid := h.series(admin, "paid_orders", from, utcToday())
	if paid.Body["total"] != float64(1) {
		t.Errorf("paid_orders = %v, want only the live one (%s)", paid.Body["total"], paid.Raw)
	}
	revenue := h.series(admin, "revenue", from, utcToday())
	if want := liveAmount * 100; revenue.Body["total"] != want || revenue.Body["unit"] != "paise" {
		t.Errorf("revenue = %v %v, want %v paise", revenue.Body["total"], revenue.Body["unit"], want)
	}

	// The CSV is the same series, rupees for money, and every export is recorded.
	csv := h.get(fmt.Sprintf("/api/admin/dashboards/metrics/revenue/export?from=%s&to=%s", from, utcToday()), admin)
	if csv.Status != http.StatusOK {
		t.Fatalf("export: %d %s", csv.Status, csv.Raw)
	}
	lines := strings.Split(strings.TrimSpace(csv.Raw), "\n")
	if lines[0] != "date,amount_inr" || len(lines) != 8 {
		t.Errorf("csv = %q", csv.Raw)
	}
	if want := fmt.Sprintf("%s,%.2f", utcToday(), liveAmount); !strings.Contains(csv.Raw, want) {
		t.Errorf("csv missing %q: %s", want, csv.Raw)
	}
	var exports int
	if err := h.pool.QueryRow(h.baseCtx,
		`SELECT count(*) FROM admin_data_exports WHERE metric = 'revenue' AND row_count = 7`).Scan(&exports); err != nil {
		t.Fatalf("read audit: %v", err)
	}
	if exports != 1 {
		t.Errorf("audit rows = %d, want 1", exports)
	}
}

func TestDashboardSeriesRefusesBadWindowsAndOutsiders(t *testing.T) {
	h := newHarness(t)
	admin := h.makeAdmin("+919000001011")
	user, _ := h.signInByPhone("+919000001012", "")

	for _, q := range []string{"", "from=" + utcToday(), "from=" + utcToday() + "&to=" + daysAgo(1), "from=" + daysAgo(400) + "&to=" + utcToday()} {
		if r := h.get("/api/admin/dashboards/metrics/signups?"+q, admin); r.Status != http.StatusBadRequest {
			t.Errorf("window %q: want 400, got %d %s", q, r.Status, r.Raw)
		}
	}
	if r := h.series(admin, "drop_table", daysAgo(1), utcToday()); r.Status != http.StatusNotFound {
		t.Errorf("unknown metric: want 404, got %d", r.Status)
	}
	if r := h.series(user, "signups", daysAgo(1), utcToday()); r.Status != http.StatusForbidden {
		t.Errorf("customer: want 403, got %d", r.Status)
	}
	if r := h.get("/api/admin/dashboards", user); r.Status != http.StatusForbidden {
		t.Errorf("customer listing dashboards: want 403, got %d", r.Status)
	}

	// The catalog is a bare array, which response.Body (a map) cannot hold.
	cat := h.get("/api/admin/dashboards/metrics", admin)
	if cat.Status != http.StatusOK || !strings.Contains(cat.Raw, `"key":"revenue"`) ||
		strings.Contains(cat.Raw, `"allowed":false`) {
		t.Errorf("catalog for an admin: %d %s", cat.Status, cat.Raw)
	}
}

// Save → export → import round-trips, dashboards are private to their owner,
// and an import is validated as strictly as a hand-built dashboard.
func TestDashboardsSaveExportImport(t *testing.T) {
	h := newHarness(t)
	alice := h.makeAdmin("+919000001021")
	bob := h.makeAdmin("+919000001022")

	widgets := []map[string]any{
		{"metric": "revenue", "viz": "KPI", "range": "30d"},
		{"metric": "signups", "viz": "line", "range": "90d", "title": " New users "},
	}
	created := h.post("/api/admin/dashboards", alice, map[string]any{"name": "  Growth ", "widgets": widgets})
	if created.Status != http.StatusCreated {
		t.Fatalf("create: %d %s", created.Status, created.Raw)
	}
	id := int64(created.Body["id"].(float64))
	if created.Body["name"] != "Growth" {
		t.Errorf("name not trimmed: %v", created.Body["name"])
	}
	ws := listOf(created.Body["widgets"])
	if ws[0]["viz"] != "kpi" || ws[1]["title"] != "New users" {
		t.Errorf("widgets not normalised: %v", ws)
	}

	// Bob cannot see, change, export or delete Alice's dashboard.
	path := fmt.Sprintf("/api/admin/dashboards/%d", id)
	for _, r := range []response{
		h.get(path, bob),
		h.get(path+"/export", bob),
		h.do(http.MethodPut, path, bob, map[string]any{"name": "Mine", "widgets": widgets}),
		h.do(http.MethodDelete, path, bob, nil),
	} {
		if r.Status != http.StatusNotFound {
			t.Errorf("another admin: want 404, got %d %s", r.Status, r.Raw)
		}
	}

	exported := h.get(path+"/export", alice)
	if exported.Status != http.StatusOK || exported.Body["format"] != models.DashboardDefinitionFormat {
		t.Fatalf("export: %d %s", exported.Status, exported.Raw)
	}
	if _, leaked := exported.Body["id"]; leaked {
		t.Errorf("a definition carries no id: %s", exported.Raw)
	}

	imported := h.post("/api/admin/dashboards/import", bob, exported.Body)
	if imported.Status != http.StatusCreated {
		t.Fatalf("import: %d %s", imported.Status, imported.Raw)
	}
	if imported.Body["name"] != "Growth" || len(listOf(imported.Body["widgets"])) != 2 {
		t.Errorf("imported = %s", imported.Raw)
	}
	if int64(imported.Body["id"].(float64)) == id {
		t.Errorf("an import is a copy, not the same row")
	}

	bad := []map[string]any{
		{"format": "something.else", "version": 1, "name": "X", "widgets": []any{}},
		{"format": models.DashboardDefinitionFormat, "version": 2, "name": "X", "widgets": []any{}},
		{"format": models.DashboardDefinitionFormat, "version": 1, "name": "X",
			"widgets": []any{map[string]any{"metric": "SELECT 1", "viz": "kpi", "range": "30d"}}},
		{"format": models.DashboardDefinitionFormat, "version": 1, "name": "",
			"widgets": []any{}},
	}
	for i, b := range bad {
		if r := h.post("/api/admin/dashboards/import", bob, b); r.Status != http.StatusBadRequest {
			t.Errorf("bad import %d: want 400, got %d %s", i, r.Status, r.Raw)
		}
	}

	// Alice edits and deletes her own.
	upd := h.do(http.MethodPut, path, alice, map[string]any{"name": "Growth v2", "widgets": widgets[:1]})
	if upd.Status != http.StatusOK || len(listOf(upd.Body["widgets"])) != 1 {
		t.Errorf("update: %d %s", upd.Status, upd.Raw)
	}
	if r := h.do(http.MethodDelete, path, alice, nil); r.Status != http.StatusNoContent {
		t.Errorf("delete: %d %s", r.Status, r.Raw)
	}
	if r := h.get(path, alice); r.Status != http.StatusNotFound {
		t.Errorf("after delete: %d", r.Status)
	}
}
