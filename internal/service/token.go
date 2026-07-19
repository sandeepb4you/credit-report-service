package service

import (
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"credit-report-service/internal/apperr"
	"credit-report-service/internal/config"
)

// TokenService issues and validates HS256 JWTs for authenticated sessions.
// The subject claim (`sub`) carries the account id.
type TokenService struct {
	secret []byte
	ttl    time.Duration
}

func NewTokenService(cfg config.AuthConfig) *TokenService {
	ttl := cfg.JWTTTL
	if ttl <= 0 {
		ttl = 720 * time.Hour
	}
	return &TokenService{secret: []byte(cfg.JWTSecret), ttl: ttl}
}

// IssuedToken is the login/verify response payload.
type IssuedToken struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expiresAt"`
}

// Issue mints a signed JWT for the given account id.
func (s *TokenService) Issue(accountID int64) (*IssuedToken, error) {
	now := time.Now().UTC()
	exp := now.Add(s.ttl)
	claims := jwt.RegisteredClaims{
		Subject:   fmt.Sprintf("%d", accountID),
		IssuedAt:  jwt.NewNumericDate(now),
		ExpiresAt: jwt.NewNumericDate(exp),
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := tok.SignedString(s.secret)
	if err != nil {
		return nil, fmt.Errorf("sign token: %w", err)
	}
	return &IssuedToken{Token: signed, ExpiresAt: exp}, nil
}

// Parse validates a token string and returns the account id from its subject.
// Any failure maps to a 401-style OtpFailure-free unauthorized error.
func (s *TokenService) Parse(tokenStr string) (int64, error) {
	claims := &jwt.RegisteredClaims{}
	_, err := jwt.ParseWithClaims(tokenStr, claims, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return s.secret, nil
	})
	if err != nil {
		return 0, apperr.NewUnauthorized("Invalid or expired session")
	}
	var accountID int64
	if _, err := fmt.Sscanf(claims.Subject, "%d", &accountID); err != nil || accountID <= 0 {
		return 0, apperr.NewUnauthorized("Invalid session subject")
	}
	return accountID, nil
}
