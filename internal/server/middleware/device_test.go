package middleware

import (
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"

	"credit-report-service/internal/models"
)

// capture runs a request through a handler that records the parsed device.
func capture(t *testing.T, headers map[string]string) models.DeviceInfo {
	t.Helper()
	var got models.DeviceInfo
	app := fiber.New()
	app.Get("/", func(c *fiber.Ctx) error {
		got = Device(c)
		return c.SendString("ok")
	})
	req := httptest.NewRequest("GET", "/", nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if _, err := app.Test(req); err != nil {
		t.Fatal(err)
	}
	return got
}

func TestDevice_ReadsHeaders(t *testing.T) {
	got := capture(t, map[string]string{
		HeaderDeviceID:        "7f3a9c21-aaaa-bbbb",
		HeaderDeviceName:      "Revanth's iPhone",
		HeaderDevicePlatform:  "ios",
		fiber.HeaderUserAgent: "Dart/3.2",
	})
	if got.DeviceID != "7f3a9c21-aaaa-bbbb" {
		t.Errorf("DeviceID = %q", got.DeviceID)
	}
	if got.Name != "Revanth's iPhone" {
		t.Errorf("Name = %q", got.Name)
	}
	if got.Platform != models.PlatformIOS {
		t.Errorf("Platform = %q", got.Platform)
	}
	if got.UserAgent != "Dart/3.2" {
		t.Errorf("UserAgent = %q", got.UserAgent)
	}
}

// Platform drives cookie-vs-JSON delivery, so casing and padding must not
// change the decision, and an unknown value must not become "web".
func TestDevice_NormalizesPlatform(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"ios", models.PlatformIOS},
		{"IOS", models.PlatformIOS},
		{"  Android ", models.PlatformAndroid},
		{"Web", models.PlatformWeb},
		{"windows-phone", ""},
		{"", ""},
	}
	for _, tt := range tests {
		got := capture(t, map[string]string{HeaderDevicePlatform: tt.in})
		if got.Platform != tt.want {
			t.Errorf("platform %q -> %q, want %q", tt.in, got.Platform, tt.want)
		}
	}
}

func TestDevice_IsWebOnlyForWeb(t *testing.T) {
	for _, p := range []string{"ios", "android", "", "nonsense"} {
		if capture(t, map[string]string{HeaderDevicePlatform: p}).IsWeb() {
			t.Errorf("platform %q must not count as web", p)
		}
	}
	if !capture(t, map[string]string{HeaderDevicePlatform: "web"}).IsWeb() {
		t.Error("platform web must count as web")
	}
}

// Oversized headers are truncated rather than rejected: a silly device name
// must never be the reason a login fails.
func TestDevice_TruncatesOversizedHeaders(t *testing.T) {
	long := strings.Repeat("x", 500)
	got := capture(t, map[string]string{
		HeaderDeviceID:   long,
		HeaderDeviceName: long,
	})
	if len(got.DeviceID) != maxDeviceHeader {
		t.Errorf("DeviceID len = %d, want %d", len(got.DeviceID), maxDeviceHeader)
	}
	if len(got.Name) != maxDeviceHeader {
		t.Errorf("Name len = %d, want %d", len(got.Name), maxDeviceHeader)
	}
}

// ---- X-Device-Info -------------------------------------------------------

func TestDevice_ParsesDeviceInfoHeader(t *testing.T) {
	got := capture(t, map[string]string{
		HeaderDevicePlatform: "ios",
		HeaderDeviceInfo:     `{"manufacturer":"Apple","model":"iPhone15,3","osVersion":"17.4","appVersion":"1.2.0"}`,
	})
	want := map[string]string{
		"manufacturer": "Apple",
		"model":        "iPhone15,3",
		"osVersion":    "17.4",
		"appVersion":   "1.2.0",
	}
	for k, v := range want {
		if got.Meta.Client[k] != v {
			t.Errorf("client[%q] = %q, want %q", k, got.Meta.Client[k], v)
		}
	}
}

// Keys are free-form so clients can add their own without a server change.
func TestDeviceInfo_AllowsArbitraryKeys(t *testing.T) {
	got := parseDeviceInfoHeader(`{"batteryTier":"high","carrier":"Jio"}`)
	if got["batteryTier"] != "high" || got["carrier"] != "Jio" {
		t.Errorf("arbitrary keys dropped: %+v", got)
	}
}

// Junk must cost the client its device label, never its ability to log in.
func TestDeviceInfo_MalformedIsIgnored(t *testing.T) {
	for _, raw := range []string{
		"not json",
		"[1,2,3]",       // array, not an object
		`"just-string"`, // scalar
		"{",
		"",
		"   ",
	} {
		if got := parseDeviceInfoHeader(raw); got != nil {
			t.Errorf("input %q -> %+v, want nil", raw, got)
		}
	}
}

// Non-string values are dropped rather than coerced, so the stored shape is
// always map[string]string.
func TestDeviceInfo_DropsNonStringValues(t *testing.T) {
	got := parseDeviceInfoHeader(`{"model":"Pixel 8","ram":8,"rooted":true,"extra":null}`)
	if got["model"] != "Pixel 8" {
		t.Errorf("model = %q", got["model"])
	}
	if len(got) != 1 {
		t.Errorf("expected only the string value to survive, got %+v", got)
	}
}

func TestDeviceInfo_RejectsOversizedPayload(t *testing.T) {
	big := `{"model":"` + strings.Repeat("x", maxDeviceInfoBytes) + `"}`
	if got := parseDeviceInfoHeader(big); got != nil {
		t.Errorf("oversized payload should be dropped whole, got %d keys", len(got))
	}
}

func TestDeviceInfo_CapsKeyCount(t *testing.T) {
	var sb strings.Builder
	sb.WriteString("{")
	for i := 0; i < maxDeviceInfoKeys+10; i++ {
		if i > 0 {
			sb.WriteString(",")
		}
		fmt.Fprintf(&sb, `"k%02d":"v"`, i)
	}
	sb.WriteString("}")

	got := parseDeviceInfoHeader(sb.String())
	if len(got) != maxDeviceInfoKeys {
		t.Errorf("kept %d keys, want cap of %d", len(got), maxDeviceInfoKeys)
	}
	// Retention is sorted, so the surviving subset is stable across requests
	// rather than flickering with Go's map ordering.
	if _, ok := got["k00"]; !ok {
		t.Error("expected the lowest-sorted keys to be retained deterministically")
	}
}

func TestDeviceInfo_CapsValueAndKeyLength(t *testing.T) {
	long := strings.Repeat("v", maxDeviceInfoValLen*2)
	got := parseDeviceInfoHeader(`{"model":"` + long + `"}`)
	if len(got["model"]) != maxDeviceInfoValLen {
		t.Errorf("value len = %d, want %d", len(got["model"]), maxDeviceInfoValLen)
	}

	longKey := strings.Repeat("k", maxDeviceInfoKeyLen+1)
	if got := parseDeviceInfoHeader(`{"` + longKey + `":"v"}`); got != nil {
		t.Errorf("oversized key should be dropped, got %+v", got)
	}
}

// A browser sends no X-Device-Info, but the UA still yields an agent
// breakdown — the two halves are independent.
func TestDevice_AgentPopulatedWithoutClientInfo(t *testing.T) {
	got := capture(t, map[string]string{
		HeaderDevicePlatform: "web",
		fiber.HeaderUserAgent: "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) " +
			"AppleWebKit/537.36 (KHTML, like Gecko) Chrome/121.0.0.0 Safari/537.36",
	})
	if got.Meta.Client != nil {
		t.Errorf("client should be nil without the header, got %+v", got.Meta.Client)
	}
	if got.Meta.Agent == nil || got.Meta.Agent.Browser != "Chrome" {
		t.Errorf("expected the agent half to be populated, got %+v", got.Meta.Agent)
	}
	if got.Meta.IsEmpty() {
		t.Error("meta with an agent must not report empty")
	}
}

func TestDevice_MetaEmptyWhenNothingSupplied(t *testing.T) {
	if !capture(t, nil).Meta.IsEmpty() {
		t.Error("expected empty meta when no headers are sent")
	}
}

func TestDevice_MissingHeadersAreEmpty(t *testing.T) {
	got := capture(t, nil)
	if got.DeviceID != "" || got.Name != "" || got.Platform != "" {
		t.Errorf("expected empty device, got %+v", got)
	}
}
