package server

import "testing"

func TestParseSize(t *testing.T) {
	cases := []struct {
		in   string
		want int
		ok   bool
	}{
		{"1024", 1024, true},
		{"5MB", 5 * 1000 * 1000, true},
		{"10MiB", 10 * 1024 * 1024, true},
		{"5M", 5 * 1024 * 1024, true},
		{"3KB", 3000, true},
		{"", 0, false},
		{"abc", 0, false},
	}
	for _, tc := range cases {
		got, ok := parseSize(tc.in)
		if ok != tc.ok || (ok && got != tc.want) {
			t.Errorf("parseSize(%q) = (%d,%v), want (%d,%v)",
				tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

func TestBodyLimitBytes_Default(t *testing.T) {
	if got := bodyLimitBytes("garbage"); got != 10*1024*1024 {
		t.Errorf("default body limit = %d, want %d", got, 10*1024*1024)
	}
}
