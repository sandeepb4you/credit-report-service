package handler

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"

	"credit-report-service/internal/apperr"
	"credit-report-service/internal/config"
	"credit-report-service/internal/models"
	"credit-report-service/internal/server/middleware"
	"credit-report-service/internal/service"
)

// newApp creates a Fiber app with the apperr.ErrorHandler registered so typed
// errors map to the correct HTTP status codes.
func newApp() *fiber.App {
	return fiber.New(fiber.Config{ErrorHandler: apperr.ErrorHandler})
}

// ---- signup handler tests ----

func TestSignup_MissingBody(t *testing.T) {
	h := NewAuthHandler(nil, nil, false)
	app := newApp()
	app.Post("/api/auth/signup", h.Signup)

	req := httptest.NewRequest("POST", "/api/auth/signup", bytes.NewReader([]byte("not-json")))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 400 {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

func TestSignup_InvalidEmail(t *testing.T) {
	h := NewAuthHandler(nil, nil, false)
	app := newApp()
	app.Post("/api/auth/signup", h.Signup)

	body, _ := json.Marshal(map[string]string{"email": "not-an-email", "password": "12345678"})
	req := httptest.NewRequest("POST", "/api/auth/signup", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 400 {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

// ---- verify-email handler tests ----

func TestVerifyEmail_MissingOTP(t *testing.T) {
	h := NewAuthHandler(nil, nil, false)
	app := newApp()
	app.Post("/api/auth/verify-email", h.VerifyEmail)

	body, _ := json.Marshal(map[string]string{"email": "user@example.com"})
	req := httptest.NewRequest("POST", "/api/auth/verify-email", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 400 {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

func TestVerifyEmail_InvalidOTPFormat(t *testing.T) {
	h := NewAuthHandler(nil, nil, false)
	app := newApp()
	app.Post("/api/auth/verify-email", h.VerifyEmail)

	body, _ := json.Marshal(map[string]string{"email": "user@example.com", "otp": "abc"})
	req := httptest.NewRequest("POST", "/api/auth/verify-email", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 400 {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

// ---- login handler tests ----

func TestLogin_EmptyFields(t *testing.T) {
	h := NewAuthHandler(nil, nil, false)
	app := newApp()
	app.Post("/api/auth/login", h.Login)

	body, _ := json.Marshal(map[string]string{"email": "", "password": ""})
	req := httptest.NewRequest("POST", "/api/auth/login", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 400 {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

func TestLogin_MissingBody(t *testing.T) {
	h := NewAuthHandler(nil, nil, false)
	app := newApp()
	app.Post("/api/auth/login", h.Login)

	req := httptest.NewRequest("POST", "/api/auth/login", bytes.NewReader([]byte("}")))
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 400 {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

// ---- resend handler tests ----

func TestResendOTP_InvalidEmail(t *testing.T) {
	h := NewAuthHandler(nil, nil, false)
	app := newApp()
	app.Post("/api/auth/otp/resend", h.ResendOTP)

	body, _ := json.Marshal(map[string]string{"email": "bad"})
	req := httptest.NewRequest("POST", "/api/auth/otp/resend", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 400 {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

// ---- google login handler tests ----

func TestGoogleLogin_MissingBody(t *testing.T) {
	h := NewAuthHandler(nil, nil, false)
	app := newApp()
	app.Post("/api/auth/google", h.GoogleLogin)

	req := httptest.NewRequest("POST", "/api/auth/google", bytes.NewReader([]byte("not-json")))
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 400 {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

// ---- profile handler tests ----

func TestGetProfile_Unauthenticated(t *testing.T) {
	h := NewAuthHandler(nil, nil, false)
	app := newApp()
	app.Get("/api/profile", h.GetProfile)

	req := httptest.NewRequest("GET", "/api/profile", nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 401 {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
}

func TestUpdateProfile_Unauthenticated(t *testing.T) {
	h := NewAuthHandler(nil, nil, false)
	app := newApp()
	app.Put("/api/profile", h.UpdateProfile)

	req := httptest.NewRequest("PUT", "/api/profile", nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 401 {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
}

func TestUpdateProfile_MissingNames(t *testing.T) {
	h := NewAuthHandler(nil, nil, false)
	app := newApp()
	app.Use(func(c *fiber.Ctx) error {
		c.Locals("accountID", int64(1))
		return c.Next()
	})
	app.Put("/api/profile", h.UpdateProfile)

	body, _ := json.Marshal(map[string]string{})
	req := httptest.NewRequest("PUT", "/api/profile", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 400 {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

func TestUpdateProfile_BadDOB(t *testing.T) {
	h := NewAuthHandler(nil, nil, false)
	app := newApp()
	app.Use(func(c *fiber.Ctx) error {
		c.Locals("accountID", int64(1))
		return c.Next()
	})
	app.Put("/api/profile", h.UpdateProfile)

	body, _ := json.Marshal(map[string]string{
		"firstName": "Ada", "lastName": "Lovelace", "dateOfBirth": "not-a-date",
	})
	req := httptest.NewRequest("PUT", "/api/profile", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 400 {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

// ---- looksLikeEmail ----

func TestLooksLikeEmail(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"user@example.com", true},
		{"a@b.co", true},
		{"@example.com", false},
		{"user@", false},
		{"no-at", false},
		{"a@@b.com", false},
		{"", false},
		// The column is VARCHAR(255) and Postgres rejects rather than
		// truncates, so an over-long address must fail here — otherwise it
		// reaches the database and becomes a 500 instead of a 400.
		{strings.Repeat("a", maxEmailLen-len("@e.com")) + "@e.com", true},
		{strings.Repeat("a", maxEmailLen) + "@e.com", false},
	}
	for _, tc := range cases {
		got := looksLikeEmail(tc.in)
		if got != tc.want {
			t.Errorf("looksLikeEmail(len=%d) = %v, want %v", len(tc.in), got, tc.want)
		}
	}
}

// ---- health handler tests ----

func TestHealth_Ping(t *testing.T) {
	h := NewHealthHandler()
	app := fiber.New()
	app.Get("/ping", h.Ping)

	req := httptest.NewRequest("GET", "/ping", nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	var m map[string]string
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("json decode: %v", err)
	}
	if m["status"] != "UP" {
		t.Errorf("status = %q, want UP", m["status"])
	}
	if m["service"] != "credit-report-service" {
		t.Errorf("service = %q", m["service"])
	}
}

// ---- credit analytics handler tests ----

func TestCreditAnalytics_Request_Unauthenticated(t *testing.T) {
	h := NewCreditAnalyticsHandler(nil)
	app := newApp()
	app.Post("/api/credit-analytics/request", h.Request)

	req := httptest.NewRequest("POST", "/api/credit-analytics/request", nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 401 {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
}

func TestCreditAnalytics_ListReports_Unauthenticated(t *testing.T) {
	h := NewCreditAnalyticsHandler(nil)
	app := newApp()
	app.Get("/api/credit-analytics/reports", h.ListReports)

	req := httptest.NewRequest("GET", "/api/credit-analytics/reports", nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 401 {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
}

func TestCreditAnalytics_GetReport_Unauthenticated(t *testing.T) {
	h := NewCreditAnalyticsHandler(nil)
	app := newApp()
	app.Get("/api/credit-analytics/reports/:id", h.GetReport)

	req := httptest.NewRequest("GET", "/api/credit-analytics/reports/abc", nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 401 {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
}

func TestCreditAnalytics_GetReport_BadID(t *testing.T) {
	h := NewCreditAnalyticsHandler(nil)
	app := newApp()
	app.Use(func(c *fiber.Ctx) error {
		c.Locals("accountID", int64(1))
		return c.Next()
	})
	app.Get("/api/credit-analytics/reports/:id", h.GetReport)

	req := httptest.NewRequest("GET", "/api/credit-analytics/reports/not-a-number", nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 400 {
		t.Errorf("status = %d, want 400 for non-integer id", resp.StatusCode)
	}
}

// ---- kyc handler tests ----

func TestKyc_SubmitPAN_Unauthenticated(t *testing.T) {
	h := NewKycHandler(nil, 0)
	app := newApp()
	app.Post("/api/kyc/pan", h.SubmitPAN)

	req := httptest.NewRequest("POST", "/api/kyc/pan", nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 401 {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
}

func TestKyc_SubmitPAN_EmptyPAN(t *testing.T) {
	h := NewKycHandler(nil, 0)
	app := newApp()
	app.Use(func(c *fiber.Ctx) error {
		c.Locals("accountID", int64(1))
		return c.Next()
	})
	app.Post("/api/kyc/pan", h.SubmitPAN)

	body, _ := json.Marshal(map[string]string{"pan": ""})
	req := httptest.NewRequest("POST", "/api/kyc/pan", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 400 {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

func TestKyc_SubmitPAN_MissingBody(t *testing.T) {
	h := NewKycHandler(nil, 0)
	app := newApp()
	app.Use(func(c *fiber.Ctx) error {
		c.Locals("accountID", int64(1))
		return c.Next()
	})
	app.Post("/api/kyc/pan", h.SubmitPAN)

	req := httptest.NewRequest("POST", "/api/kyc/pan", bytes.NewReader([]byte("not-json")))
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 400 {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

func TestKyc_VerifyPAN_Unauthenticated(t *testing.T) {
	h := NewKycHandler(nil, 0)
	app := newApp()
	app.Post("/api/admin/kyc/pan/:accountId/verify", h.VerifyPAN)

	req := httptest.NewRequest("POST", "/api/admin/kyc/pan/1/verify", nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 401 {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
}

func TestKyc_VerifyPAN_BadAccountID(t *testing.T) {
	h := NewKycHandler(nil, 0)
	app := newApp()
	app.Use(func(c *fiber.Ctx) error {
		// The handler records the caller as the reviewer, so it needs the id the
		// real middleware would have published, not just the role.
		c.Locals("accountID", int64(1))
		c.Locals("accountRole", "admin")
		return c.Next()
	})
	app.Post("/api/admin/kyc/pan/:accountId/verify", h.VerifyPAN)

	req := httptest.NewRequest("POST", "/api/admin/kyc/pan/abc/verify", nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 400 {
		t.Errorf("status = %d, want 400 for non-integer accountId", resp.StatusCode)
	}
}

// ---- Middleware: RequireAuth/RequireRole + AccountID/AccountRole ----

func TestRequireAuth_MissingHeader(t *testing.T) {
	tokens := service.NewTokenService(config.AuthConfig{JWTSecret: "secret", AccessTTL: time.Hour})
	app := fiber.New(fiber.Config{ErrorHandler: apperr.ErrorHandler})
	app.Use(middleware.RequireAuth(tokens))
	app.Get("/", func(c *fiber.Ctx) error { return c.SendString("ok") })

	req := httptest.NewRequest("GET", "/", nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 401 {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
}

func TestRequireAuth_ValidToken(t *testing.T) {
	tokens := service.NewTokenService(config.AuthConfig{JWTSecret: "secret", AccessTTL: time.Hour})
	issued, err := tokens.Issue(42, "admin", 0, 0)
	if err != nil {
		t.Fatal(err)
	}

	app := fiber.New()
	app.Use(middleware.RequireAuth(tokens))
	app.Get("/", func(c *fiber.Ctx) error {
		id, ok := middleware.AccountID(c)
		if !ok || id != 42 {
			return fiber.NewError(500, "bad account id")
		}
		role, ok := middleware.AccountRole(c)
		if !ok || role != "admin" {
			return fiber.NewError(500, "bad role")
		}
		return c.SendString("ok")
	})

	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer "+issued.Token)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
}

func TestRequireRole_WrongRole(t *testing.T) {
	tokens := service.NewTokenService(config.AuthConfig{JWTSecret: "secret", AccessTTL: time.Hour})
	issued, _ := tokens.Issue(1, "user", 0, 0)

	app := fiber.New(fiber.Config{ErrorHandler: apperr.ErrorHandler})
	app.Use(middleware.RequireRole(tokens, nil, "admin"))
	app.Get("/", func(c *fiber.Ctx) error { return c.SendString("ok") })

	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer "+issued.Token)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 403 {
		t.Errorf("status = %d, want 403", resp.StatusCode)
	}
}

// ---- RequirePermission ---------------------------------------------------

// The capability gate is what routes actually use, so it gets the same
// fail-closed scrutiny as the role gate.
func TestRequirePermission(t *testing.T) {
	tests := []struct {
		name string
		role string
		perm string
		want int
	}{
		{"agent may create coupons", models.RoleAgent, models.PermCouponCreate, 200},
		{"admin inherits coupon create", models.RoleAdmin, models.PermCouponCreate, 200},
		{"user may not create coupons", models.RoleUser, models.PermCouponCreate, 403},
		{"legacy empty role may not", "", models.PermCouponCreate, 403},
		{"agent may not verify kyc", models.RoleAgent, models.PermKycVerify, 403},
		{"agent may not grant roles", models.RoleAgent, models.PermAccountSetRole, 403},
		{"admin may grant roles", models.RoleAdmin, models.PermAccountSetRole, 200},
		{"unknown role denied", "superuser", models.PermCouponCreate, 403},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tokens := service.NewTokenService(config.AuthConfig{JWTSecret: "secret", AccessTTL: time.Hour})
			issued, _ := tokens.Issue(1, tt.role, 0, 0)

			app := fiber.New(fiber.Config{ErrorHandler: apperr.ErrorHandler})
			app.Use(middleware.RequirePermission(tokens, nil, tt.perm))
			app.Get("/", func(c *fiber.Ctx) error { return c.SendString("ok") })

			req := httptest.NewRequest("GET", "/", nil)
			req.Header.Set("Authorization", "Bearer "+issued.Token)
			resp, err := app.Test(req)
			if err != nil {
				t.Fatal(err)
			}
			if resp.StatusCode != tt.want {
				t.Errorf("status = %d, want %d", resp.StatusCode, tt.want)
			}
		})
	}
}

// A bad token is 401 (not authenticated), never 403 (authenticated but
// unauthorized) — the two must stay distinguishable to clients.
func TestRequirePermission_NoTokenIs401(t *testing.T) {
	tokens := service.NewTokenService(config.AuthConfig{JWTSecret: "secret", AccessTTL: time.Hour})
	app := fiber.New(fiber.Config{ErrorHandler: apperr.ErrorHandler})
	app.Use(middleware.RequirePermission(tokens, nil, models.PermCouponCreate))
	app.Get("/", func(c *fiber.Ctx) error { return c.SendString("ok") })

	resp, err := app.Test(httptest.NewRequest("GET", "/", nil))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 401 {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
}

// RequireRole is a minimum-rank check, so a higher role passes a lower gate.
func TestRequireRole_HigherRolePasses(t *testing.T) {
	tokens := service.NewTokenService(config.AuthConfig{JWTSecret: "secret", AccessTTL: time.Hour})
	issued, _ := tokens.Issue(1, models.RoleAdmin, 0, 0)

	app := fiber.New(fiber.Config{ErrorHandler: apperr.ErrorHandler})
	app.Use(middleware.RequireRole(tokens, nil, models.RoleUser))
	app.Get("/", func(c *fiber.Ctx) error { return c.SendString("ok") })

	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer "+issued.Token)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Errorf("status = %d, want 200 (admin satisfies a user gate)", resp.StatusCode)
	}
}

// A token minted before the role claim existed carries no role; it must still
// satisfy a RoleUser gate and still be refused at a RoleAdmin gate.
func TestRequireRole_LegacyTokenIsUser(t *testing.T) {
	tokens := service.NewTokenService(config.AuthConfig{JWTSecret: "secret", AccessTTL: time.Hour})
	issued, _ := tokens.Issue(1, "", 0, 0)

	cases := []struct {
		gate string
		want int
	}{
		{models.RoleUser, 200},
		{models.RoleAdmin, 403},
	}
	for _, tc := range cases {
		app := fiber.New(fiber.Config{ErrorHandler: apperr.ErrorHandler})
		app.Use(middleware.RequireRole(tokens, nil, tc.gate))
		app.Get("/", func(c *fiber.Ctx) error { return c.SendString("ok") })

		req := httptest.NewRequest("GET", "/", nil)
		req.Header.Set("Authorization", "Bearer "+issued.Token)
		resp, err := app.Test(req)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != tc.want {
			t.Errorf("gate %q: status = %d, want %d", tc.gate, resp.StatusCode, tc.want)
		}
	}
}

// An unrecognized role must fail closed rather than pass a lower gate.
func TestRequireRole_UnknownRoleDenied(t *testing.T) {
	tokens := service.NewTokenService(config.AuthConfig{JWTSecret: "secret", AccessTTL: time.Hour})
	issued, _ := tokens.Issue(1, "superuser", 0, 0)

	app := fiber.New(fiber.Config{ErrorHandler: apperr.ErrorHandler})
	app.Use(middleware.RequireRole(tokens, nil, models.RoleUser))
	app.Get("/", func(c *fiber.Ctx) error { return c.SendString("ok") })

	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer "+issued.Token)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 403 {
		t.Errorf("status = %d, want 403 for an unknown role", resp.StatusCode)
	}
}

// RequireAuth normalizes a missing role claim to RoleUser in Locals.
func TestRequireAuth_LegacyTokenRoleNormalized(t *testing.T) {
	tokens := service.NewTokenService(config.AuthConfig{JWTSecret: "secret", AccessTTL: time.Hour})
	issued, _ := tokens.Issue(7, "", 0, 0)

	app := fiber.New(fiber.Config{ErrorHandler: apperr.ErrorHandler})
	app.Use(middleware.RequireAuth(tokens))
	app.Get("/", func(c *fiber.Ctx) error {
		role, ok := middleware.AccountRole(c)
		if !ok || role != models.RoleUser {
			return fiber.NewError(500, "role = "+role)
		}
		return c.SendString("ok")
	})

	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer "+issued.Token)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
}

func TestRequireAuth_InvalidScheme(t *testing.T) {
	tokens := service.NewTokenService(config.AuthConfig{JWTSecret: "secret", AccessTTL: time.Hour})
	app := fiber.New(fiber.Config{ErrorHandler: apperr.ErrorHandler})
	app.Use(middleware.RequireAuth(tokens))
	app.Get("/", func(c *fiber.Ctx) error { return c.SendString("ok") })

	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Basic abc")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 401 {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
}

// ---- Stub mailer used by tests ----

type stubMailer struct {
	lastOTP   string
	lastDest  string
	lastKind  string // "signup" | "password_reset"
	sendErr   error
	sentCount int
}

func (m *stubMailer) SendOTP(dest, otp string) error {
	return m.record(dest, otp, "signup")
}

func (m *stubMailer) SendPasswordResetOTP(dest, otp string) error {
	return m.record(dest, otp, "password_reset")
}

func (m *stubMailer) record(dest, otp, kind string) error {
	m.lastDest = dest
	m.lastOTP = otp
	m.lastKind = kind
	m.sentCount++
	return m.sendErr
}

func (m *stubMailer) Config() (string, string, int) { return "", "", 587 }
func (m *stubMailer) Close() error                  { return nil }

func TestStubMailer_Records(t *testing.T) {
	m := &stubMailer{}
	if err := m.SendOTP("a@b.com", "123456"); err != nil {
		t.Fatal(err)
	}
	if m.lastDest != "a@b.com" || m.lastOTP != "123456" {
		t.Errorf("lastDest=%q lastOTP=%q", m.lastDest, m.lastOTP)
	}
	if m.sentCount != 1 {
		t.Errorf("sentCount = %d, want 1", m.sentCount)
	}
	if m.lastKind != "signup" {
		t.Errorf("lastKind = %q, want %q", m.lastKind, "signup")
	}
	// A reset code must be distinguishable from a signup code, or the stub can
	// satisfy the Mailer interface while the user gets the wrong email.
	if err := m.SendPasswordResetOTP("a@b.com", "654321"); err != nil {
		t.Fatal(err)
	}
	if m.lastKind != "password_reset" || m.lastOTP != "654321" {
		t.Errorf("lastKind=%q lastOTP=%q", m.lastKind, m.lastOTP)
	}
}

func TestStubMailer_Close(t *testing.T) {
	m := &stubMailer{}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
}

// ---- phone registration handler tests ----
//
// The service call needs a database, so these cover what the handler decides on
// its own: that a session is required, and that a malformed body or OTP is
// rejected before the service is reached (h.svc is nil, so anything that got
// through would panic rather than pass).

func TestSendPhoneRegistration_Unauthenticated(t *testing.T) {
	h := NewAuthHandler(nil, nil, false)
	app := newApp()
	app.Post("/api/auth/phone/send", h.SendPhoneRegistration)

	body, _ := json.Marshal(map[string]string{"phone": "9876543210"})
	req := httptest.NewRequest("POST", "/api/auth/phone/send", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 401 {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
}

func TestSendPhoneRegistration_BadBody(t *testing.T) {
	h := NewAuthHandler(nil, nil, false)
	app := newApp()
	app.Use(func(c *fiber.Ctx) error {
		c.Locals("accountID", int64(1))
		return c.Next()
	})
	app.Post("/api/auth/phone/send", h.SendPhoneRegistration)

	req := httptest.NewRequest("POST", "/api/auth/phone/send", bytes.NewReader([]byte("not-json")))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 400 {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

func TestVerifyPhoneRegistration_Unauthenticated(t *testing.T) {
	h := NewAuthHandler(nil, nil, false)
	app := newApp()
	app.Post("/api/auth/phone/verify", h.VerifyPhoneRegistration)

	body, _ := json.Marshal(map[string]string{"phone": "9876543210", "otp": "1234"})
	req := httptest.NewRequest("POST", "/api/auth/phone/verify", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 401 {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
}

func TestVerifyPhoneRegistration_BadOTP(t *testing.T) {
	h := NewAuthHandler(nil, nil, false)
	app := newApp()
	app.Use(func(c *fiber.Ctx) error {
		c.Locals("accountID", int64(1))
		return c.Next()
	})
	app.Post("/api/auth/phone/verify", h.VerifyPhoneRegistration)

	for _, otp := range []string{"", "12", "abcd", "123456789"} {
		body, _ := json.Marshal(map[string]string{"phone": "9876543210", "otp": otp})
		req := httptest.NewRequest("POST", "/api/auth/phone/verify", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		resp, err := app.Test(req)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != 400 {
			t.Errorf("otp %q: status = %d, want 400", otp, resp.StatusCode)
		}
	}
}

// ---- email link handler tests ----
//
// As with the phone pair, the service call needs a database, so these cover what
// the handler settles on its own (h.svc is nil, so anything reaching it panics
// rather than passing).

func TestSendEmailLink_Unauthenticated(t *testing.T) {
	h := NewAuthHandler(nil, nil, false)
	app := newApp()
	app.Post("/api/auth/email/send", h.SendEmailLink)

	body, _ := json.Marshal(map[string]string{"email": "user@example.com"})
	req := httptest.NewRequest("POST", "/api/auth/email/send", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 401 {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
}

func TestSendEmailLink_InvalidEmail(t *testing.T) {
	h := NewAuthHandler(nil, nil, false)
	app := newApp()
	app.Use(func(c *fiber.Ctx) error {
		c.Locals("accountID", int64(1))
		return c.Next()
	})
	app.Post("/api/auth/email/send", h.SendEmailLink)

	for _, email := range []string{"", "not-an-email", "@example.com"} {
		body, _ := json.Marshal(map[string]string{"email": email})
		req := httptest.NewRequest("POST", "/api/auth/email/send", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		resp, err := app.Test(req)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != 400 {
			t.Errorf("email %q: status = %d, want 400", email, resp.StatusCode)
		}
	}
}

func TestVerifyEmailLink_Unauthenticated(t *testing.T) {
	h := NewAuthHandler(nil, nil, false)
	app := newApp()
	app.Post("/api/auth/email/verify", h.VerifyEmailLink)

	body, _ := json.Marshal(map[string]string{"email": "user@example.com", "otp": "1234"})
	req := httptest.NewRequest("POST", "/api/auth/email/verify", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 401 {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
}

func TestVerifyEmailLink_BadOTP(t *testing.T) {
	h := NewAuthHandler(nil, nil, false)
	app := newApp()
	app.Use(func(c *fiber.Ctx) error {
		c.Locals("accountID", int64(1))
		return c.Next()
	})
	app.Post("/api/auth/email/verify", h.VerifyEmailLink)

	for _, otp := range []string{"", "12", "abcd", "123456789"} {
		body, _ := json.Marshal(map[string]string{"email": "user@example.com", "otp": otp})
		req := httptest.NewRequest("POST", "/api/auth/email/verify", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		resp, err := app.Test(req)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != 400 {
			t.Errorf("otp %q: status = %d, want 400", otp, resp.StatusCode)
		}
	}
}
