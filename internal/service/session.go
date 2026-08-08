package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"time"

	"credit-report-service/internal/apperr"
	"credit-report-service/internal/config"
	"credit-report-service/internal/models"
	"credit-report-service/internal/repository"
)

// refreshTokenBytes is the entropy behind a refresh token. 32 bytes (256 bits)
// makes guessing infeasible and lets the digest be a plain SHA-256.
const refreshTokenBytes = 32

// refreshTokenPrefix marks the token in logs and client storage. It is part of
// the token string and therefore part of the hashed value.
const refreshTokenPrefix = "rt_"

// SessionService owns the long-lived half of authentication: opaque refresh
// tokens bound to a device row. The short-lived access JWT is minted by
// TokenService and carries this session's id in its `sid` claim, which is what
// lets a request know which device it came from.
type SessionService struct {
	repo *repository.SessionRepo
	ttl  time.Duration
}

func NewSessionService(repo *repository.SessionRepo, cfg config.AuthConfig) *SessionService {
	ttl := cfg.RefreshTTL
	if ttl <= 0 {
		ttl = 720 * time.Hour
	}
	return &SessionService{repo: repo, ttl: ttl}
}

// SessionView is the device-list projection returned to clients. It is a
// separate type from models.Session so no future column (a token digest, an
// internal flag) can leak into a response by being added to the row model.
type SessionView struct {
	ID         int64     `json:"id"`
	DeviceName string    `json:"deviceName"`
	Platform   string    `json:"platform"`
	IP         string    `json:"ip"`
	CreatedAt  time.Time `json:"signedInAt"`
	LastUsedAt time.Time `json:"lastUsedAt"`
	ExpiresAt  time.Time `json:"expiresAt"`
	// DeviceInfo is the make/model/OS breakdown, split into what the client
	// declared and what the server parsed from the User-Agent.
	DeviceInfo *models.DeviceMeta `json:"deviceInfo,omitempty"`
	// Current marks the session the requesting device is using right now, so
	// the UI can label it and warn before revoking it.
	Current bool `json:"current"`
}

// Start opens a session for a device and returns it with the plaintext refresh
// token. The plaintext exists only in this return value — the row holds a
// SHA-256 digest — so it can never be recovered or re-shown later.
func (s *SessionService) Start(
	ctx context.Context, accountID int64, dev models.DeviceInfo,
) (*models.Session, string, error) {
	token, digest, err := newRefreshToken()
	if err != nil {
		return nil, "", err
	}

	row := sessionRow(dev)
	row.AccountID = accountID
	row.DeviceID = nullable(dev.DeviceID)
	row.DevicePlatform = nullable(dev.Platform)
	row.ExpiresAt = time.Now().UTC().Add(s.ttl)
	if err := s.repo.Create(ctx, row, digest); err != nil {
		return nil, "", err
	}
	slog.Info("session started",
		"account_id", accountID, "session_id", row.ID, "platform", dev.Platform)
	return row, token, nil
}

// Refresh exchanges a refresh token for the next one, rotating the stored
// digest so each token is single-use.
//
// Replay of an already-rotated token means the token leaked (the legitimate
// client would hold the newer one), so the whole session is revoked rather
// than merely rejected. That turns a stolen refresh token into at most one
// extra access-token lifetime of exposure and logs the device out for real.
func (s *SessionService) Refresh(
	ctx context.Context, token string, dev models.DeviceInfo,
) (*models.Session, string, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return nil, "", apperr.NewUnauthorized("Refresh token required")
	}
	digest := hashToken(token)

	row, err := s.repo.FindLiveByHash(ctx, digest)
	if errors.Is(err, repository.ErrNotFound) {
		return nil, "", s.handleUnknownToken(ctx, digest)
	}
	if err != nil {
		return nil, "", err
	}

	next, nextDigest, err := newRefreshToken()
	if err != nil {
		return nil, "", err
	}
	rotated, err := s.repo.Rotate(ctx, row.ID, digest, nextDigest,
		time.Now().UTC().Add(s.ttl), sessionRow(dev))
	if errors.Is(err, repository.ErrNotFound) {
		// Lost a race with a concurrent refresh on the same token. The other
		// caller already rotated; this one must not mint a second token.
		return nil, "", apperr.NewUnauthorized("Session expired; please sign in again")
	}
	if err != nil {
		return nil, "", err
	}
	slog.Info("session refreshed", "account_id", rotated.AccountID, "session_id", rotated.ID)
	return rotated, next, nil
}

// handleUnknownToken distinguishes a replayed (already-rotated) token from one
// that is merely unknown or expired. Both return 401; the replay additionally
// revokes the session.
func (s *SessionService) handleUnknownToken(ctx context.Context, digest string) error {
	replayed, err := s.repo.FindByPrevHash(ctx, digest)
	if errors.Is(err, repository.ErrNotFound) {
		return apperr.NewUnauthorized("Session expired; please sign in again")
	}
	if err != nil {
		return err
	}
	if _, err := s.repo.Revoke(ctx, replayed.AccountID, replayed.ID, models.RevokeTokenReuse); err != nil {
		slog.Error("failed to revoke session after token reuse",
			"account_id", replayed.AccountID, "session_id", replayed.ID, "error", err)
	}
	// Loud on purpose: this is the signal that a refresh token leaked.
	slog.Warn("refresh token reuse detected; session revoked",
		"account_id", replayed.AccountID, "session_id", replayed.ID)
	return apperr.NewUnauthorized("Session expired; please sign in again")
}

// List returns the account's signed-in devices, flagging the current one.
func (s *SessionService) List(
	ctx context.Context, accountID, currentID int64,
) ([]SessionView, error) {
	rows, err := s.repo.ListLive(ctx, accountID)
	if err != nil {
		return nil, err
	}
	out := make([]SessionView, 0, len(rows))
	for _, r := range rows {
		out = append(out, SessionView{
			ID:         r.ID,
			DeviceName: deref(r.DeviceName),
			Platform:   deref(r.DevicePlatform),
			IP:         deref(r.IP),
			CreatedAt:  r.CreatedAt,
			LastUsedAt: r.LastUsedAt,
			ExpiresAt:  r.ExpiresAt,
			DeviceInfo: decodeMeta(r.DeviceMeta),
			Current:    r.ID == currentID,
		})
	}
	return out, nil
}

// Revoke signs an account out of one device. The account scoping lives in the
// repository query, so a caller cannot revoke a session it does not own — a
// wrong id is indistinguishable from a missing one.
func (s *SessionService) Revoke(ctx context.Context, accountID, sessionID int64, reason string) error {
	ok, err := s.repo.Revoke(ctx, accountID, sessionID, reason)
	if err != nil {
		return err
	}
	if !ok {
		return apperr.NewNotFound("No such active session")
	}
	slog.Info("session revoked",
		"account_id", accountID, "session_id", sessionID, "reason", reason)
	return nil
}

// RevokeAllForAccount signs the account out of every device, including the one
// making the request. Unlike RevokeOthers this is not a user-initiated device
// cleanup but a security action (see models.RevokePasswordReset), so the caller
// supplies the reason that will be recorded on each row.
//
// Access tokens already minted stay valid until they expire — revocation acts
// on the refresh half of the pair, as everywhere else in this service.
func (s *SessionService) RevokeAllForAccount(
	ctx context.Context, accountID int64, reason string,
) (int64, error) {
	n, err := s.repo.RevokeAll(ctx, accountID, 0, reason)
	if err != nil {
		return 0, err
	}
	slog.Info("all sessions revoked", "account_id", accountID, "count", n, "reason", reason)
	return n, nil
}

// RevokeOthers signs the account out everywhere except keepID (0 revokes all).
func (s *SessionService) RevokeOthers(ctx context.Context, accountID, keepID int64) (int64, error) {
	n, err := s.repo.RevokeAll(ctx, accountID, keepID, models.RevokeAllOthers)
	if err != nil {
		return 0, err
	}
	slog.Info("sessions revoked", "account_id", accountID, "count", n, "kept", keepID)
	return n, nil
}

// ---- row helpers ---------------------------------------------------------

// sessionRow projects a request's device metadata onto the mutable columns of
// a session row. Fields the client omitted stay nil so the repository's
// COALESCE keeps whatever was already stored rather than blanking it.
func sessionRow(dev models.DeviceInfo) *models.Session {
	row := &models.Session{
		DeviceName: nullable(deviceLabel(dev)),
		UserAgent:  nullable(truncate(dev.UserAgent, 512)),
		IP:         nullable(dev.IP),
	}
	if !dev.Meta.IsEmpty() {
		// A marshal failure here is not worth failing a login over: the meta is
		// decorative, so drop it and keep the previously stored value.
		if raw, err := json.Marshal(dev.Meta); err == nil {
			row.DeviceMeta = raw
		} else {
			slog.Warn("device metadata marshal failed; keeping stored value", "error", err)
		}
	}
	return row
}

// decodeMeta turns a stored jsonb payload back into a DeviceMeta. Unreadable
// payloads render as absent rather than failing the whole device list.
func decodeMeta(raw []byte) *models.DeviceMeta {
	if len(raw) == 0 {
		return nil
	}
	var m models.DeviceMeta
	if err := json.Unmarshal(raw, &m); err != nil {
		slog.Warn("stored device metadata is unreadable", "error", err)
		return nil
	}
	if m.IsEmpty() {
		return nil
	}
	return &m
}

// ---- token helpers -------------------------------------------------------

// newRefreshToken mints a token and its storage digest.
func newRefreshToken() (token, digest string, err error) {
	buf := make([]byte, refreshTokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", "", err
	}
	token = refreshTokenPrefix + base64.RawURLEncoding.EncodeToString(buf)
	return token, hashToken(token), nil
}

// hashToken is the one place a refresh token becomes a stored value.
func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// deviceLabel picks the name shown in the device list, falling back to the
// platform and then to a generic label so no entry renders blank.
func deviceLabel(dev models.DeviceInfo) string {
	if n := strings.TrimSpace(dev.Name); n != "" {
		return truncate(n, 128)
	}
	switch dev.Platform {
	case models.PlatformIOS:
		return "iOS device"
	case models.PlatformAndroid:
		return "Android device"
	case models.PlatformWeb:
		return "Web browser"
	}
	return "Unknown device"
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
