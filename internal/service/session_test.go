package service

import (
	"strings"
	"testing"

	"credit-report-service/internal/models"
)

// Every refresh token must be unique and unpredictable — this is the whole
// security of the long-lived half of the token pair.
func TestNewRefreshToken_UniqueAndPrefixed(t *testing.T) {
	seen := make(map[string]bool, 500)
	for i := 0; i < 500; i++ {
		token, digest, err := newRefreshToken()
		if err != nil {
			t.Fatalf("newRefreshToken: %v", err)
		}
		if !strings.HasPrefix(token, refreshTokenPrefix) {
			t.Fatalf("token %q missing prefix", token)
		}
		if seen[token] {
			t.Fatalf("duplicate token generated at iteration %d", i)
		}
		seen[token] = true
		if digest != hashToken(token) {
			t.Fatal("returned digest does not match hashToken of the token")
		}
	}
}

// The stored digest must not be reversible to, or equal to, the token itself:
// a database leak must not hand over usable credentials.
func TestHashToken_IsDigestNotToken(t *testing.T) {
	token, digest, err := newRefreshToken()
	if err != nil {
		t.Fatal(err)
	}
	if digest == token {
		t.Fatal("digest must differ from the token")
	}
	if strings.Contains(digest, strings.TrimPrefix(token, refreshTokenPrefix)) {
		t.Fatal("digest must not contain the raw token")
	}
	if len(digest) != 64 {
		t.Errorf("digest len = %d, want 64 (sha256 hex)", len(digest))
	}
}

func TestHashToken_Deterministic(t *testing.T) {
	if hashToken("rt_abc") != hashToken("rt_abc") {
		t.Error("hashToken must be deterministic")
	}
	if hashToken("rt_abc") == hashToken("rt_abd") {
		t.Error("distinct tokens must hash differently")
	}
}

// No entry in the device list may render blank, so a missing name falls back
// to the platform and then to a generic label.
func TestDeviceLabel_Fallbacks(t *testing.T) {
	tests := []struct {
		name string
		dev  models.DeviceInfo
		want string
	}{
		{"explicit name wins", models.DeviceInfo{Name: "Pixel 8", Platform: "android"}, "Pixel 8"},
		{"blank name falls back to platform", models.DeviceInfo{Name: "  ", Platform: "ios"}, "iOS device"},
		{"android fallback", models.DeviceInfo{Platform: models.PlatformAndroid}, "Android device"},
		{"web fallback", models.DeviceInfo{Platform: models.PlatformWeb}, "Web browser"},
		{"nothing at all", models.DeviceInfo{}, "Unknown device"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := deviceLabel(tt.dev); got != tt.want {
				t.Errorf("deviceLabel = %q, want %q", got, tt.want)
			}
		})
	}
}

// Device names are client-supplied; they must be clipped to the column width
// rather than overflowing the insert.
func TestDeviceLabel_TruncatesLongName(t *testing.T) {
	got := deviceLabel(models.DeviceInfo{Name: strings.Repeat("z", 400)})
	if len(got) != 128 {
		t.Errorf("label len = %d, want 128", len(got))
	}
}

// ---- device metadata -----------------------------------------------------

func TestSessionRow_MarshalsMetaRoundTrip(t *testing.T) {
	dev := models.DeviceInfo{
		Name:      "Pixel 8",
		UserAgent: "Mozilla/5.0",
		IP:        "203.0.113.7",
		Meta: models.DeviceMeta{
			Client: map[string]string{"manufacturer": "Google", "model": "Pixel 8"},
			Agent:  &models.AgentMeta{Browser: "Chrome", OS: "Android", DeviceType: models.DeviceTypeMobile},
		},
	}
	row := sessionRow(dev)
	if len(row.DeviceMeta) == 0 {
		t.Fatal("expected marshalled device meta")
	}

	back := decodeMeta(row.DeviceMeta)
	if back == nil {
		t.Fatal("decodeMeta returned nil")
	}
	if back.Client["manufacturer"] != "Google" || back.Client["model"] != "Pixel 8" {
		t.Errorf("client half lost: %+v", back.Client)
	}
	if back.Agent == nil || back.Agent.Browser != "Chrome" || back.Agent.OS != "Android" {
		t.Errorf("agent half lost: %+v", back.Agent)
	}
}

// Fields the client omitted stay nil so the repository's COALESCE keeps the
// stored value instead of blanking a populated row on a sparse refresh.
func TestSessionRow_OmittedFieldsStayNil(t *testing.T) {
	row := sessionRow(models.DeviceInfo{})
	if row.IP != nil {
		t.Errorf("IP = %v, want nil", *row.IP)
	}
	if row.UserAgent != nil {
		t.Errorf("UserAgent = %v, want nil", *row.UserAgent)
	}
	if row.DeviceMeta != nil {
		t.Errorf("DeviceMeta = %s, want nil", row.DeviceMeta)
	}
}

func TestDecodeMeta_EmptyAndGarbage(t *testing.T) {
	if got := decodeMeta(nil); got != nil {
		t.Errorf("nil -> %+v, want nil", got)
	}
	if got := decodeMeta([]byte("")); got != nil {
		t.Errorf("empty -> %+v, want nil", got)
	}
	if got := decodeMeta([]byte("{not json")); got != nil {
		t.Errorf("garbage -> %+v, want nil (must not fail the device list)", got)
	}
	if got := decodeMeta([]byte("{}")); got != nil {
		t.Errorf("empty object -> %+v, want nil", got)
	}
}

func TestTruncate(t *testing.T) {
	if got := truncate("abc", 10); got != "abc" {
		t.Errorf("short string changed: %q", got)
	}
	if got := truncate("abcdef", 3); got != "abc" {
		t.Errorf("truncate = %q, want abc", got)
	}
}
