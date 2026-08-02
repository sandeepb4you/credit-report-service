package middleware

import (
	"strings"

	"credit-report-service/internal/models"
)

// parseUserAgent derives a browser/OS breakdown from a User-Agent string.
//
// This is deliberately a small hand-rolled matcher rather than a UA database:
// the output is display-only ("Chrome on macOS" in a device list), so breadth
// matters far less than staying dependency-free and predictable. Anything it
// does not recognize yields empty fields — it never guesses, because a wrong
// device label is worse than a blank one when the user is deciding whether a
// session is theirs.
//
// Returns nil when nothing at all could be identified.
func parseUserAgent(ua string) *models.AgentMeta {
	if strings.TrimSpace(ua) == "" {
		return nil
	}
	m := &models.AgentMeta{}
	m.Browser, m.BrowserVersion = parseBrowser(ua)
	m.OS, m.OSVersion = parseOS(ua)
	m.DeviceType = parseDeviceType(ua)

	if m.Browser == "" && m.OS == "" && m.DeviceType == "" {
		return nil
	}
	return m
}

// browserTokens are checked in order. Order is load-bearing: Edge and Opera
// both carry "Chrome" in their UA, and Chrome itself carries "Safari", so the
// more specific token has to win first.
var browserTokens = []struct{ token, name string }{
	{"Edg/", "Edge"},
	{"EdgiOS/", "Edge"},
	{"OPR/", "Opera"},
	{"SamsungBrowser/", "Samsung Internet"},
	{"CriOS/", "Chrome"},  // Chrome on iOS
	{"FxiOS/", "Firefox"}, // Firefox on iOS
	{"Firefox/", "Firefox"},
	{"Chrome/", "Chrome"},
	{"Version/", "Safari"}, // Safari puts its own version under Version/
}

func parseBrowser(ua string) (name, version string) {
	for _, b := range browserTokens {
		if v, ok := versionAfter(ua, b.token); ok {
			// "Version/" only means Safari when Safari/ is actually present;
			// other agents reuse the token.
			if b.token == "Version/" && !strings.Contains(ua, "Safari/") {
				continue
			}
			return b.name, v
		}
	}
	if strings.Contains(ua, "Safari/") {
		return "Safari", ""
	}
	return "", ""
}

func parseOS(ua string) (name, version string) {
	switch {
	case strings.Contains(ua, "Windows NT"):
		v, _ := versionAfter(ua, "Windows NT ")
		// NT 10.0 covers both Windows 10 and 11 and the UA cannot tell them
		// apart, so report the kernel version rather than inventing a name.
		return "Windows", v
	case strings.Contains(ua, "Android"):
		v, _ := versionAfter(ua, "Android ")
		return "Android", v
	case strings.Contains(ua, "iPhone OS"), strings.Contains(ua, "CPU OS"):
		v, ok := versionAfter(ua, "iPhone OS ")
		if !ok {
			v, _ = versionAfter(ua, "CPU OS ")
		}
		if strings.Contains(ua, "iPad") {
			return "iPadOS", strings.ReplaceAll(v, "_", ".")
		}
		return "iOS", strings.ReplaceAll(v, "_", ".")
	case strings.Contains(ua, "Mac OS X"):
		v, _ := versionAfter(ua, "Mac OS X ")
		return "macOS", strings.ReplaceAll(v, "_", ".")
	case strings.Contains(ua, "CrOS"):
		return "ChromeOS", ""
	case strings.Contains(ua, "Linux"):
		return "Linux", ""
	}
	return "", ""
}

func parseDeviceType(ua string) string {
	switch {
	case strings.Contains(ua, "iPad"), strings.Contains(ua, "Tablet"):
		return models.DeviceTypeTablet
	// Android without "Mobile" is conventionally a tablet.
	case strings.Contains(ua, "Android") && !strings.Contains(ua, "Mobile"):
		return models.DeviceTypeTablet
	case strings.Contains(ua, "Mobile"), strings.Contains(ua, "iPhone"),
		strings.Contains(ua, "Android"):
		return models.DeviceTypeMobile
	case strings.Contains(ua, "Windows"), strings.Contains(ua, "Macintosh"),
		strings.Contains(ua, "CrOS"), strings.Contains(ua, "Linux"):
		return models.DeviceTypeDesktop
	}
	return ""
}

// maxVersionLen caps a parsed version so a hostile User-Agent cannot push a
// long run of digits into the stored payload.
const maxVersionLen = 24

// versionAfter returns the version literal following token, stopping at the
// first character that cannot appear in one.
func versionAfter(ua, token string) (string, bool) {
	i := strings.Index(ua, token)
	if i < 0 {
		return "", false
	}
	rest := ua[i+len(token):]
	end := 0
	for end < len(rest) && end < maxVersionLen {
		c := rest[end]
		if (c >= '0' && c <= '9') || c == '.' || c == '_' {
			end++
			continue
		}
		break
	}
	if end == 0 {
		return "", true // token present but no parsable version
	}
	return strings.Trim(rest[:end], "._"), true
}
