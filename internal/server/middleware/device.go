package middleware

import (
	"encoding/json"
	"sort"
	"strings"

	"github.com/gofiber/fiber/v2"

	"credit-report-service/internal/models"
)

// Device metadata headers sent by clients on auth calls.
const (
	HeaderDeviceID       = "X-Device-Id"
	HeaderDeviceName     = "X-Device-Name"
	HeaderDevicePlatform = "X-Device-Platform"
	// HeaderDeviceInfo carries a small JSON object describing the device, e.g.
	//   {"manufacturer":"Apple","model":"iPhone15,3","osVersion":"17.4"}
	// Keys are free-form so a client can add its own without a server change;
	// see parseDeviceInfoHeader for the bounds.
	HeaderDeviceInfo = "X-Device-Info"
)

// maxDeviceHeader bounds each header so a client cannot push oversized values
// into the sessions table. Values are truncated, not rejected: a bad device
// label must never be the reason a login fails.
const maxDeviceHeader = 128

// Bounds on the X-Device-Info payload. This is unauthenticated user input
// heading for a jsonb column on every login, so it is capped on every axis:
// total bytes, key count, and the length of each key and value.
const (
	maxDeviceInfoBytes  = 1024
	maxDeviceInfoKeys   = 12
	maxDeviceInfoKeyLen = 32
	maxDeviceInfoValLen = 128
)

// Device builds the device descriptor for the current request.
//
// Every field here is client-controlled and therefore untrusted. It exists so
// the user can recognize their own devices in the session list; it grants
// nothing. The one field that changes server behaviour is Platform == "web",
// which switches refresh-token delivery to an httpOnly cookie.
func Device(c *fiber.Ctx) models.DeviceInfo {
	ua := c.Get(fiber.HeaderUserAgent)
	return models.DeviceInfo{
		DeviceID:  clip(c.Get(HeaderDeviceID)),
		Name:      clip(c.Get(HeaderDeviceName)),
		Platform:  normalizePlatform(c.Get(HeaderDevicePlatform)),
		UserAgent: ua,
		IP:        c.IP(),
		Meta: models.DeviceMeta{
			Client: parseDeviceInfoHeader(c.Get(HeaderDeviceInfo)),
			Agent:  parseUserAgent(ua),
		},
	}
}

// parseDeviceInfoHeader decodes the X-Device-Info JSON object into a bounded
// string map.
//
// Every failure mode returns nil rather than an error: this is decorative
// metadata, and a client sending junk must lose its device label, not its
// ability to log in. Non-string values are dropped instead of coerced, so the
// stored shape is always map[string]string.
func parseDeviceInfoHeader(raw string) map[string]string {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > maxDeviceInfoBytes {
		return nil
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		return nil
	}

	// Sort keys so the retained subset is deterministic when a client sends
	// more than the cap — otherwise which keys survive would vary per request
	// and the device list would flicker.
	keys := make([]string, 0, len(decoded))
	for k := range decoded {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	out := make(map[string]string, len(keys))
	for _, k := range keys {
		if len(out) >= maxDeviceInfoKeys {
			break
		}
		s, ok := decoded[k].(string)
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		s = strings.TrimSpace(s)
		if k == "" || s == "" || len(k) > maxDeviceInfoKeyLen {
			continue
		}
		out[k] = truncateTo(s, maxDeviceInfoValLen)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// normalizePlatform accepts only the known platforms and discards anything
// else, so the stored value is always renderable and "web" cannot be spoofed
// by casing tricks.
func normalizePlatform(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case models.PlatformIOS:
		return models.PlatformIOS
	case models.PlatformAndroid:
		return models.PlatformAndroid
	case models.PlatformWeb:
		return models.PlatformWeb
	}
	return ""
}

func clip(v string) string {
	return truncateTo(strings.TrimSpace(v), maxDeviceHeader)
}

func truncateTo(v string, max int) string {
	if len(v) <= max {
		return v
	}
	return v[:max]
}
