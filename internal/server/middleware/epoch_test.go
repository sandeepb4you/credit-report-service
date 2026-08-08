package middleware

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"

	"credit-report-service/internal/apperr"
	"credit-report-service/internal/config"
	"credit-report-service/internal/models"
	"credit-report-service/internal/service"
)

type stubEpochs struct {
	epoch int
	err   error
	calls int
}

func (s *stubEpochs) TokenEpoch(context.Context, int64) (int, error) {
	s.calls++
	return s.epoch, s.err
}

func epochApp(h fiber.Handler) *fiber.App {
	app := fiber.New(fiber.Config{ErrorHandler: apperr.ErrorHandler})
	app.Get("/gated", h, func(c *fiber.Ctx) error { return c.SendString("ok") })
	return app
}

func newTokens() *service.TokenService {
	return service.NewTokenService(config.AuthConfig{JWTSecret: "epoch-test", AccessTTL: time.Hour})
}

func TestRequirePermission_EpochMatches(t *testing.T) {
	tokens := newTokens()
	issued, _ := tokens.Issue(1, models.RoleAdmin, 1, 7)
	epochs := &stubEpochs{epoch: 7}

	app := epochApp(RequirePermission(tokens, epochs, models.PermKycVerify))
	req := httptest.NewRequest("GET", "/gated", nil)
	req.Header.Set("Authorization", "Bearer "+issued.Token)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
}

// The point of the whole mechanism: a token minted before the role changed must
// be refused, and with 401 — clients treat that as "refresh and retry", so the
// user is not made to sign in again.
func TestRequirePermission_StaleEpochIs401(t *testing.T) {
	tokens := newTokens()
	issued, _ := tokens.Issue(1, models.RoleAdmin, 1, 7)
	epochs := &stubEpochs{epoch: 8} // SetRole bumped it after the token was minted

	app := epochApp(RequirePermission(tokens, epochs, models.PermKycVerify))
	req := httptest.NewRequest("GET", "/gated", nil)
	req.Header.Set("Authorization", "Bearer "+issued.Token)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 401 {
		t.Errorf("status = %d, want 401 (403 would make clients sign the user out)", resp.StatusCode)
	}
}

// A demoted admin's token still says "admin". The epoch check is what stops it,
// so this is the case that closes the demotion window.
func TestRequirePermission_DemotedAdminRefused(t *testing.T) {
	tokens := newTokens()
	stale, _ := tokens.Issue(1, models.RoleAdmin, 1, 3) // token says admin
	epochs := &stubEpochs{epoch: 4}                     // demoted since

	app := epochApp(RequirePermission(tokens, epochs, models.PermAccountSetRole))
	req := httptest.NewRequest("GET", "/gated", nil)
	req.Header.Set("Authorization", "Bearer "+stale.Token)
	resp, _ := app.Test(req)
	if resp.StatusCode != 401 {
		t.Errorf("status = %d, want 401 — a demoted admin must not keep access", resp.StatusCode)
	}
}

// A lookup failure must deny, not admit: this is a permission gate.
func TestRequirePermission_EpochLookupFailureDenies(t *testing.T) {
	tokens := newTokens()
	issued, _ := tokens.Issue(1, models.RoleAdmin, 1, 0)
	epochs := &stubEpochs{err: errors.New("db down")}

	app := epochApp(RequirePermission(tokens, epochs, models.PermKycVerify))
	req := httptest.NewRequest("GET", "/gated", nil)
	req.Header.Set("Authorization", "Bearer "+issued.Token)
	resp, _ := app.Test(req)
	if resp.StatusCode != 401 {
		t.Errorf("status = %d, want 401 on lookup failure (fail closed)", resp.StatusCode)
	}
}

// RequireAuth makes no authorization decision from the role, so it must not pay
// for the lookup — this is what keeps the hot paths at zero extra queries.
func TestRequireAuth_DoesNotQueryEpoch(t *testing.T) {
	tokens := newTokens()
	issued, _ := tokens.Issue(1, models.RoleUser, 1, 99)
	epochs := &stubEpochs{epoch: 1} // deliberately mismatched

	app := epochApp(RequireAuth(tokens))
	req := httptest.NewRequest("GET", "/gated", nil)
	req.Header.Set("Authorization", "Bearer "+issued.Token)
	resp, _ := app.Test(req)
	if resp.StatusCode != 200 {
		t.Errorf("status = %d, want 200: RequireAuth must not check the epoch", resp.StatusCode)
	}
	if epochs.calls != 0 {
		t.Errorf("epoch lookups = %d, want 0 on a RequireAuth route", epochs.calls)
	}
}

// A nil source disables the check so the middleware stays usable without a DB.
func TestRequirePermission_NilEpochSourceSkipsCheck(t *testing.T) {
	tokens := newTokens()
	issued, _ := tokens.Issue(1, models.RoleAdmin, 1, 5)

	app := epochApp(RequirePermission(tokens, nil, models.PermKycVerify))
	req := httptest.NewRequest("GET", "/gated", nil)
	req.Header.Set("Authorization", "Bearer "+issued.Token)
	resp, _ := app.Test(req)
	if resp.StatusCode != 200 {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
}
