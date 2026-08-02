package service

import (
	"errors"
	"testing"
	"time"

	"credit-report-service/internal/apperr"
	"credit-report-service/internal/config"
)

func TestToken_IssueAndParse_RoundTrip(t *testing.T) {
	svc := NewTokenService(config.AuthConfig{JWTSecret: "test-secret-key", AccessTTL: time.Hour})
	issued, err := svc.Issue(42, "admin", 0)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if issued.Token == "" {
		t.Fatal("expected non-empty token")
	}
	if issued.ExpiresAt.Before(time.Now().UTC()) {
		t.Fatal("ExpiresAt should be in the future")
	}

	accountID, role, _, err := svc.Parse(issued.Token)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if accountID != 42 {
		t.Errorf("accountID = %d, want 42", accountID)
	}
	if role != "admin" {
		t.Errorf("role = %q, want admin", role)
	}
}

func TestToken_IssueAndParse_UserRole(t *testing.T) {
	svc := NewTokenService(config.AuthConfig{JWTSecret: "secret", AccessTTL: time.Hour})
	issued, _ := svc.Issue(7, "user", 0)
	_, role, _, _ := svc.Parse(issued.Token)
	if role != "user" {
		t.Errorf("role = %q, want user", role)
	}
}

func TestToken_IssueAndParse_EmptyRole(t *testing.T) {
	svc := NewTokenService(config.AuthConfig{JWTSecret: "secret", AccessTTL: time.Hour})
	issued, _ := svc.Issue(1, "", 0)
	_, role, _, _ := svc.Parse(issued.Token)
	if role != "" {
		t.Errorf("role = %q, want empty", role)
	}
}

func TestToken_Parse_Expired(t *testing.T) {
	svc := NewTokenService(config.AuthConfig{JWTSecret: "test-secret", AccessTTL: time.Millisecond})
	issued, err := svc.Issue(1, "user", 0)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	time.Sleep(5 * time.Millisecond) // ensure expiry
	_, _, _, err = svc.Parse(issued.Token)
	if !isUnauthorized(err) {
		t.Fatalf("expired token should be Unauthorized, got %v", err)
	}
}

func TestToken_Parse_WrongKey(t *testing.T) {
	svc1 := NewTokenService(config.AuthConfig{JWTSecret: "secret-one", AccessTTL: time.Hour})
	svc2 := NewTokenService(config.AuthConfig{JWTSecret: "secret-two", AccessTTL: time.Hour})
	issued, err := svc1.Issue(1, "user", 0)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	_, _, _, err = svc2.Parse(issued.Token)
	if !isUnauthorized(err) {
		t.Fatalf("wrong key should be Unauthorized, got %v", err)
	}
}

func TestToken_Parse_Garbage(t *testing.T) {
	svc := NewTokenService(config.AuthConfig{JWTSecret: "test-secret", AccessTTL: time.Hour})
	_, _, _, err := svc.Parse("not-a-jwt")
	if !isUnauthorized(err) {
		t.Fatalf("garbage should be Unauthorized, got %v", err)
	}
}

func TestToken_Parse_Empty(t *testing.T) {
	svc := NewTokenService(config.AuthConfig{JWTSecret: "test-secret", AccessTTL: time.Hour})
	_, _, _, err := svc.Parse("")
	if !isUnauthorized(err) {
		t.Fatalf("empty should be Unauthorized, got %v", err)
	}
}

// An unset TTL must fall back to the short access lifetime, never to a
// long-lived one — a misconfiguration should fail safe.
func TestToken_DefaultTTL(t *testing.T) {
	svc := NewTokenService(config.AuthConfig{JWTSecret: "secret", AccessTTL: 0})
	issued, err := svc.Issue(1, "user", 0)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	diff := time.Until(issued.ExpiresAt)
	expected := 15 * time.Minute
	if diff < expected-time.Minute || diff > expected+time.Minute {
		t.Errorf("ExpiresAt diff = %v, expected ~%v", diff, expected)
	}
}

func TestToken_DefaultTTL_NegativeInput(t *testing.T) {
	svc := NewTokenService(config.AuthConfig{JWTSecret: "secret", AccessTTL: -time.Hour})
	issued, err := svc.Issue(1, "user", 0)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	diff := time.Until(issued.ExpiresAt)
	if diff <= 0 || diff > 16*time.Minute {
		t.Errorf("negative TTL should default to 15m, got diff=%v", diff)
	}
}

// The session id must survive the round trip: it is how a request knows which
// device it came from, and every device-management endpoint depends on it.
func TestToken_SessionIDRoundTrip(t *testing.T) {
	svc := NewTokenService(config.AuthConfig{JWTSecret: "secret", AccessTTL: time.Hour})
	issued, err := svc.Issue(42, "user", 987)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	accountID, role, sid, err := svc.Parse(issued.Token)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if accountID != 42 || role != "user" || sid != 987 {
		t.Errorf("got (%d, %q, %d), want (42, \"user\", 987)", accountID, role, sid)
	}
}

// A token minted before session tracking has no sid claim; it must parse with
// a zero session id rather than failing.
func TestToken_MissingSessionIDParsesAsZero(t *testing.T) {
	svc := NewTokenService(config.AuthConfig{JWTSecret: "secret", AccessTTL: time.Hour})
	issued, _ := svc.Issue(5, "user", 0)
	_, _, sid, err := svc.Parse(issued.Token)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if sid != 0 {
		t.Errorf("sid = %d, want 0", sid)
	}
}

func isUnauthorized(err error) bool {
	var u *apperr.Unauthorized
	return errors.As(err, &u)
}
