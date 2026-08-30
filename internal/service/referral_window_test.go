package service

import (
	"testing"
	"time"
)

func day(s string) time.Time {
	t, err := time.ParseInLocation("2006-01-02", s, time.UTC)
	if err != nil {
		panic(err)
	}
	return t
}

// The default window is what the admin screen opens on, so it has to be
// exactly 30 days inclusive -- 29 days back plus today.
func TestResolveReferralWindow_DefaultsToThirtyInclusiveDays(t *testing.T) {
	from, to, err := resolveReferralWindow(time.Time{}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	days := int(to.Sub(from).Hours()/24) + 1
	if days != ReferralDefaultWindowDays {
		t.Errorf("default window is %d days, want %d", days, ReferralDefaultWindowDays)
	}
	today := time.Now().UTC().Truncate(24 * time.Hour)
	if !to.Equal(today) {
		t.Errorf("default window ends %s, want today (%s)", to, today)
	}
}

// Either bound alone has to be usable, or the two date inputs on the screen
// only work as a pair.
func TestResolveReferralWindow_FillsInTheMissingBound(t *testing.T) {
	from, to, err := resolveReferralWindow(day("2026-08-01"), time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if !from.Equal(day("2026-08-01")) {
		t.Errorf("from = %s, want it left alone", from)
	}
	if !to.Equal(time.Now().UTC().Truncate(24 * time.Hour)) {
		t.Errorf("to = %s, want today", to)
	}

	from, to, err = resolveReferralWindow(time.Time{}, day("2026-08-30"))
	if err != nil {
		t.Fatal(err)
	}
	if !to.Equal(day("2026-08-30")) {
		t.Errorf("to = %s, want it left alone", to)
	}
	if !from.Equal(day("2026-08-01")) {
		t.Errorf("from = %s, want 30 inclusive days back (2026-08-01)", from)
	}
}

func TestResolveReferralWindow_RejectsInvertedRange(t *testing.T) {
	if _, _, err := resolveReferralWindow(day("2026-08-30"), day("2026-08-01")); err == nil {
		t.Fatal("an end date before the start date should be rejected")
	}
}

// An unbounded range is a table scan an operator can trigger by typing a
// decade into a date box.
func TestResolveReferralWindow_RejectsRangeOverAYear(t *testing.T) {
	if _, _, err := resolveReferralWindow(day("2020-01-01"), day("2026-01-01")); err == nil {
		t.Fatal("a six-year range should be rejected")
	}
	if _, _, err := resolveReferralWindow(day("2026-01-01"), day("2026-12-31")); err != nil {
		t.Fatalf("a range inside one year should be accepted: %v", err)
	}
}

// A single day must mean that day, not an empty window: [from, to+1d).
func TestResolveReferralWindow_SingleDayIsNotEmpty(t *testing.T) {
	from, to, err := resolveReferralWindow(day("2026-08-15"), day("2026-08-15"))
	if err != nil {
		t.Fatal(err)
	}
	if !from.Equal(to) {
		t.Fatalf("from %s and to %s should be the same day", from, to)
	}
	if end := to.AddDate(0, 0, 1); !end.After(from) {
		t.Errorf("half-open window [%s, %s) is empty", from, end)
	}
}
