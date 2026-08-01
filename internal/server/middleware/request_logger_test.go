package middleware

import (
	"testing"
)

func TestIsNoisy(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"/api/ping", true},
		{"/swagger/foo", true},
		{"/swagger", true},
		{"/api/auth/login", false},
		{"/api/credit-analytics/request", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := isNoisy(tc.path); got != tc.want {
			t.Errorf("isNoisy(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}
