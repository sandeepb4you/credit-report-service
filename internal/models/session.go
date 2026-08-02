package models

import "time"

// Device platforms reported by the client in the X-Device-Platform header.
// PlatformWeb is the one value that changes server behaviour: web clients get
// the refresh token as an httpOnly cookie instead of in the JSON body.
const (
	PlatformIOS     = "ios"
	PlatformAndroid = "android"
	PlatformWeb     = "web"
)

// Reasons a session was revoked, stored in sessions.revoked_reason.
const (
	RevokeLogout     = "logout"      // user signed out on this device
	RevokeByUser     = "revoked"     // user removed the device from the device list
	RevokeAllOthers  = "logout_all"  // user signed out everywhere else
	RevokeTokenReuse = "token_reuse" // a rotated refresh token was replayed
	RevokeRoleChange = "role_change" // privileges changed; force re-auth
)

// DeviceInfo is the per-request device metadata recorded on a session.
//
// DeviceID comes from the client (X-Device-Id) and is untrusted: it groups
// repeat logins from one physical device for display purposes and nothing
// more. All authority rests on the refresh token itself.
type DeviceInfo struct {
	DeviceID  string
	Name      string
	Platform  string
	UserAgent string
	IP        string
	Meta      DeviceMeta
}

// DeviceMeta is the structured device description stored in sessions.device_meta.
//
// The two halves are kept separate on purpose so it is always obvious which
// facts the client asserted and which the server derived. Nothing in here is
// trusted or used for authorization — it exists so a user can recognize
// "Chrome on macOS" or "Pixel 8, Android 14" in their device list.
type DeviceMeta struct {
	// Client is whatever the app declared via X-Device-Info: make, model, OS
	// version, app version, and any extra keys it cares to send. Free-form but
	// bounded (see middleware.parseDeviceInfoHeader) because it is user input.
	Client map[string]string `json:"client,omitempty"`
	// Agent is parsed by the server from the User-Agent header. Best-effort:
	// unrecognized agents leave fields empty rather than guessing.
	Agent *AgentMeta `json:"agent,omitempty"`
}

// AgentMeta is the User-Agent breakdown for browser (and some mobile) clients.
type AgentMeta struct {
	Browser        string `json:"browser,omitempty"`
	BrowserVersion string `json:"browserVersion,omitempty"`
	OS             string `json:"os,omitempty"`
	OSVersion      string `json:"osVersion,omitempty"`
	DeviceType     string `json:"deviceType,omitempty"` // mobile | tablet | desktop
}

// IsEmpty reports whether there is nothing worth storing.
func (m DeviceMeta) IsEmpty() bool { return len(m.Client) == 0 && m.Agent == nil }

// Device types reported in AgentMeta.DeviceType.
const (
	DeviceTypeMobile  = "mobile"
	DeviceTypeTablet  = "tablet"
	DeviceTypeDesktop = "desktop"
)

// IsWeb reports whether the client asked to be treated as a browser, which
// switches refresh-token delivery to an httpOnly cookie.
func (d DeviceInfo) IsWeb() bool { return d.Platform == PlatformWeb }

// Session is the row model for the sessions table: one signed-in device. The
// refresh-token digests are deliberately absent — they are only ever used in
// WHERE clauses inside the repository and must never reach a response.
type Session struct {
	ID        int64 `json:"id"        db:"id"`
	AccountID int64 `json:"accountId" db:"account_id"`

	DeviceID       *string `json:"deviceId"       db:"device_id"`
	DeviceName     *string `json:"deviceName"     db:"device_name"`
	DevicePlatform *string `json:"devicePlatform" db:"device_platform"`
	UserAgent      *string `json:"userAgent"      db:"user_agent"`
	IP             *string `json:"ip"             db:"ip"`
	// DeviceMeta is the raw jsonb payload; callers unmarshal it into
	// DeviceMeta. Never serialized directly — the API returns SessionView.
	DeviceMeta []byte `json:"-" db:"device_meta"`

	CreatedAt     time.Time  `json:"createdAt"     db:"created_at"`
	LastUsedAt    time.Time  `json:"lastUsedAt"    db:"last_used_at"`
	ExpiresAt     time.Time  `json:"expiresAt"     db:"expires_at"`
	RevokedAt     *time.Time `json:"revokedAt"     db:"revoked_at"`
	RevokedReason *string    `json:"revokedReason" db:"revoked_reason"`
}
