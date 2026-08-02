package service

import (
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"credit-report-service/internal/apperr"
	"credit-report-service/internal/config"
)

// TokenService issues and validates the short-lived HS256 access JWT. The
// subject claim (`sub`) carries the account id, `role` the account role, and
// `sid` the id of the session (device) the token was minted for.
//
// Access tokens are deliberately stateless: verification never touches the
// database. That means a revoked session's access token keeps working until it
// expires, which is why the TTL is minutes rather than days. The revocable
// half of the pair is the refresh token — see SessionService.
type TokenService struct {
	secret []byte
	ttl    time.Duration
}

func NewTokenService(cfg config.AuthConfig) *TokenService {
	ttl := cfg.AccessTTL
	if ttl <= 0 {
		ttl = 15 * time.Minute
	}
	return &TokenService{secret: []byte(cfg.JWTSecret), ttl: ttl}
}

// TTL is the lifetime of the access tokens this service issues.
func (s *TokenService) TTL() time.Duration { return s.ttl }

// sessionClaims embeds the standard registered claims and adds the role and
// session id. Embedding RegisteredClaims means tokens issued before either
// custom claim existed still parse (they just unmarshal to the zero value).
type sessionClaims struct {
	Role      string `json:"role,omitempty"`
	SessionID int64  `json:"sid,omitempty"`
	jwt.RegisteredClaims
}

// IssuedToken is the login/verify response payload.
type IssuedToken struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expiresAt"`
}

// Issue mints a signed access token for an account + role, bound to the
// session (device) it was issued for.
func (s *TokenService) Issue(accountID int64, role string, sessionID int64) (*IssuedToken, error) {
	now := time.Now().UTC()
	exp := now.Add(s.ttl)
	claims := sessionClaims{
		Role:      role,
		SessionID: sessionID,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   fmt.Sprintf("%d", accountID),
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(exp),
		},
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := tok.SignedString(s.secret)
	if err != nil {
		return nil, fmt.Errorf("sign token: %w", err)
	}
	return &IssuedToken{Token: signed, ExpiresAt: exp}, nil
}

// Parse validates a token string and returns the account id, role, and
// session id from its claims. Any failure maps to a 401-style unauthorized
// error. A zero session id means the token predates session tracking.
func (s *TokenService) Parse(tokenStr string) (int64, string, int64, error) {
	claims := &sessionClaims{}
	_, err := jwt.ParseWithClaims(tokenStr, claims, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return s.secret, nil
	})
	if err != nil {
		return 0, "", 0, apperr.NewUnauthorized("Invalid or expired session")
	}
	var accountID int64
	if _, err := fmt.Sscanf(claims.Subject, "%d", &accountID); err != nil || accountID <= 0 {
		return 0, "", 0, apperr.NewUnauthorized("Invalid session subject")
	}
	return accountID, claims.Role, claims.SessionID, nil
}
