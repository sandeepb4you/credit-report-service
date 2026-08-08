package handler

import (
	"testing"
	"time"
)

func TestValidateDOB(t *testing.T) {
	now := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)

	cases := []struct {
		name    string
		dob     time.Time
		wantErr bool
	}{
		{"ordinary date", time.Date(1991, 10, 6, 0, 0, 0, 0, time.UTC), false},
		{"today", now, false},
		{"yesterday", now.AddDate(0, 0, -1), false},
		{"tomorrow", now.AddDate(0, 0, 1), true},
		{"far future", time.Date(3000, 1, 1, 0, 0, 0, 0, time.UTC), true},
		{"exactly max age", now.AddDate(-maxAgeYears, 0, 0), false},
		{"a day past max age", now.AddDate(-maxAgeYears, 0, -1), true},
		{"year 1000", time.Date(1000, 1, 1, 0, 0, 0, 0, time.UTC), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msg := validateDOB(tc.dob, now)
			if gotErr := msg != ""; gotErr != tc.wantErr {
				t.Errorf("validateDOB(%s) = %q, wantErr %v",
					tc.dob.Format("2006-01-02"), msg, tc.wantErr)
			}
		})
	}
}
