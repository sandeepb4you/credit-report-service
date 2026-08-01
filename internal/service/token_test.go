package service

import (
	"errors"
	"testing"
	"time"

	"credit-report-service/internal/apperr"
	"credit-report-service/internal/config"
)

func TestToken_IssueAndParse_RoundTrip(t *testing.T) {
	svc := NewTokenService(config.AuthConfig{JWTSecret: "test-secret-key", JWTTTL: time.Hour})
	issued, err := svc.Issue(42, "admin")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if issued.Token == "" {
		t.Fatal("expected non-empty token")
	}
	if issued.ExpiresAt.Before(time.Now().UTC()) {
		t.Fatal("ExpiresAt should be in the future")
	}

	accountID, role, err := svc.Parse(issued.Token)
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
	svc := NewTokenService(config.AuthConfig{JWTSecret: "secret", JWTTTL: time.Hour})
	issued, _ := svc.Issue(7, "user")
	_, role, _ := svc.Parse(issued.Token)
	if role != "user" {
		t.Errorf("role = %q, want user", role)
	}
}

func TestToken_IssueAndParse_EmptyRole(t *testing.T) {
	svc := NewTokenService(config.AuthConfig{JWTSecret: "secret", JWTTTL: time.Hour})
	issued, _ := svc.Issue(1, "")
	_, role, _ := svc.Parse(issued.Token)
	if role != "" {
		t.Errorf("role = %q, want empty", role)
	}
}

func TestToken_Parse_Expired(t *testing.T) {
	svc := NewTokenService(config.AuthConfig{JWTSecret: "test-secret", JWTTTL: time.Millisecond})
	issued, err := svc.Issue(1, "user")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	time.Sleep(5 * time.Millisecond) // ensure expiry
	_, _, err = svc.Parse(issued.Token)
	if !isUnauthorized(err) {
		t.Fatalf("expired token should be Unauthorized, got %v", err)
	}
}

func TestToken_Parse_WrongKey(t *testing.T) {
	svc1 := NewTokenService(config.AuthConfig{JWTSecret: "secret-one", JWTTTL: time.Hour})
	svc2 := NewTokenService(config.AuthConfig{JWTSecret: "secret-two", JWTTTL: time.Hour})
	issued, err := svc1.Issue(1, "user")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	_, _, err = svc2.Parse(issued.Token)
	if !isUnauthorized(err) {
		t.Fatalf("wrong key should be Unauthorized, got %v", err)
	}
}

func TestToken_Parse_Garbage(t *testing.T) {
	svc := NewTokenService(config.AuthConfig{JWTSecret: "test-secret", JWTTTL: time.Hour})
	_, _, err := svc.Parse("not-a-jwt")
	if !isUnauthorized(err) {
		t.Fatalf("garbage should be Unauthorized, got %v", err)
	}
}

func TestToken_Parse_Empty(t *testing.T) {
	svc := NewTokenService(config.AuthConfig{JWTSecret: "test-secret", JWTTTL: time.Hour})
	_, _, err := svc.Parse("")
	if !isUnauthorized(err) {
		t.Fatalf("empty should be Unauthorized, got %v", err)
	}
}

func TestToken_DefaultTTL(t *testing.T) {
	svc := NewTokenService(config.AuthConfig{JWTSecret: "secret", JWTTTL: 0})
	issued, err := svc.Issue(1, "user")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	diff := time.Until(issued.ExpiresAt)
	expected := 720 * time.Hour
	if diff < expected-time.Minute || diff > expected+time.Minute {
		t.Errorf("ExpiresAt diff = %v, expected ~%v", diff, expected)
	}
}

func TestToken_DefaultTTL_NegativeInput(t *testing.T) {
	svc := NewTokenService(config.AuthConfig{JWTSecret: "secret", JWTTTL: -time.Hour})
	issued, err := svc.Issue(1, "user")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	diff := time.Until(issued.ExpiresAt)
	if diff < 719*time.Hour {
		t.Errorf("negative TTL should default to 720h, got diff=%v", diff)
	}
}

func isUnauthorized(err error) bool {
	var u *apperr.Unauthorized
	return errors.As(err, &u)
}
