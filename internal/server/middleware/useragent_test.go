package middleware

import (
	"testing"

	"credit-report-service/internal/models"
)

func TestParseUserAgent_RealAgents(t *testing.T) {
	tests := []struct {
		name       string
		ua         string
		browser    string
		os         string
		osVersion  string
		deviceType string
	}{
		{
			name:       "Chrome on macOS",
			ua:         "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/121.0.0.0 Safari/537.36",
			browser:    "Chrome",
			os:         "macOS",
			osVersion:  "10.15.7",
			deviceType: models.DeviceTypeDesktop,
		},
		{
			name:       "Safari on macOS",
			ua:         "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.3 Safari/605.1.15",
			browser:    "Safari",
			os:         "macOS",
			osVersion:  "10.15.7",
			deviceType: models.DeviceTypeDesktop,
		},
		{
			name:       "Safari on iPhone",
			ua:         "Mozilla/5.0 (iPhone; CPU iPhone OS 17_4 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.4 Mobile/15E148 Safari/604.1",
			browser:    "Safari",
			os:         "iOS",
			osVersion:  "17.4",
			deviceType: models.DeviceTypeMobile,
		},
		{
			name:       "Safari on iPad",
			ua:         "Mozilla/5.0 (iPad; CPU OS 17_4 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.4 Mobile/15E148 Safari/604.1",
			browser:    "Safari",
			os:         "iPadOS",
			osVersion:  "17.4",
			deviceType: models.DeviceTypeTablet,
		},
		{
			name:       "Chrome on Android phone",
			ua:         "Mozilla/5.0 (Linux; Android 14; Pixel 8) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/121.0.0.0 Mobile Safari/537.36",
			browser:    "Chrome",
			os:         "Android",
			osVersion:  "14",
			deviceType: models.DeviceTypeMobile,
		},
		{
			name:       "Chrome on Android tablet (no Mobile token)",
			ua:         "Mozilla/5.0 (Linux; Android 13; SM-X700) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/121.0.0.0 Safari/537.36",
			browser:    "Chrome",
			os:         "Android",
			osVersion:  "13",
			deviceType: models.DeviceTypeTablet,
		},
		{
			name:       "Firefox on Windows",
			ua:         "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:122.0) Gecko/20100101 Firefox/122.0",
			browser:    "Firefox",
			os:         "Windows",
			osVersion:  "10.0",
			deviceType: models.DeviceTypeDesktop,
		},
		{
			name:       "Edge on Windows",
			ua:         "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/121.0.0.0 Safari/537.36 Edg/121.0.0.0",
			browser:    "Edge",
			os:         "Windows",
			osVersion:  "10.0",
			deviceType: models.DeviceTypeDesktop,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseUserAgent(tt.ua)
			if got == nil {
				t.Fatal("expected a parse result")
			}
			if got.Browser != tt.browser {
				t.Errorf("Browser = %q, want %q", got.Browser, tt.browser)
			}
			if got.OS != tt.os {
				t.Errorf("OS = %q, want %q", got.OS, tt.os)
			}
			if got.OSVersion != tt.osVersion {
				t.Errorf("OSVersion = %q, want %q", got.OSVersion, tt.osVersion)
			}
			if got.DeviceType != tt.deviceType {
				t.Errorf("DeviceType = %q, want %q", got.DeviceType, tt.deviceType)
			}
		})
	}
}

// Edge and Opera both carry "Chrome" in their UA, and Chrome carries "Safari".
// A mislabelled browser is worse than a blank one, so ordering is pinned.
func TestParseUserAgent_MoreSpecificBrowserWins(t *testing.T) {
	tests := []struct{ ua, want string }{
		{"Mozilla/5.0 Chrome/121.0.0.0 Safari/537.36 Edg/121.0.0.0", "Edge"},
		{"Mozilla/5.0 Chrome/121.0.0.0 Safari/537.36 OPR/107.0.0.0", "Opera"},
		{"Mozilla/5.0 Chrome/121.0.0.0 Mobile Safari/537.36 SamsungBrowser/23.0", "Samsung Internet"},
		{"Mozilla/5.0 (iPhone) CriOS/121.0.0.0 Mobile/15E148 Safari/604.1", "Chrome"},
		{"Mozilla/5.0 (iPhone) FxiOS/122.0 Mobile/15E148 Safari/605.1.15", "Firefox"},
		{"Mozilla/5.0 Chrome/121.0.0.0 Safari/537.36", "Chrome"},
	}
	for _, tt := range tests {
		if got, _ := parseBrowser(tt.ua); got != tt.want {
			t.Errorf("ua %q -> %q, want %q", tt.ua, got, tt.want)
		}
	}
}

// "Version/" is Safari's own version token, but other agents reuse it — it
// only means Safari when Safari/ is actually present.
func TestParseBrowser_VersionTokenNeedsSafari(t *testing.T) {
	if got, _ := parseBrowser("SomeBot/1.0 Version/9.9"); got != "" {
		t.Errorf("got %q, want empty — Version/ alone is not Safari", got)
	}
}

// An unrecognized agent must yield nothing rather than a wrong guess.
func TestParseUserAgent_UnknownYieldsNil(t *testing.T) {
	for _, ua := range []string{"", "   ", "curl/8.4.0"} {
		if got := parseUserAgent(ua); got != nil {
			t.Errorf("ua %q -> %+v, want nil", ua, got)
		}
	}
}

// A native mobile client sends a bare SDK agent; there is no browser to name,
// but that must not produce a bogus one.
func TestParseUserAgent_NativeSDKAgent(t *testing.T) {
	got := parseUserAgent("Dart/3.2 (dart:io)")
	if got != nil && got.Browser != "" {
		t.Errorf("Browser = %q, want empty for a native SDK agent", got.Browser)
	}
}

func TestVersionAfter(t *testing.T) {
	if v, _ := versionAfter("Chrome/121.0.5 Safari", "Chrome/"); v != "121.0.5" {
		t.Errorf("got %q, want 121.0.5", v)
	}
	if v, _ := versionAfter("Mac OS X 10_15_7)", "Mac OS X "); v != "10_15_7" {
		t.Errorf("got %q, want 10_15_7", v)
	}
	if _, ok := versionAfter("nothing here", "Chrome/"); ok {
		t.Error("missing token should report not-found")
	}
}

// A hostile agent must not push an unbounded run of digits into storage.
func TestVersionAfter_CapsLength(t *testing.T) {
	ua := "Chrome/" + "1234567890.1234567890.1234567890.1234567890"
	v, _ := versionAfter(ua, "Chrome/")
	if len(v) > maxVersionLen {
		t.Errorf("version len = %d, want <= %d", len(v), maxVersionLen)
	}
}
