package handler

import (
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"

	"credit-report-service/internal/apperr"
	_ "credit-report-service/internal/models" // referenced by swag annotations (models.Account)
	"credit-report-service/internal/server/middleware"
	"credit-report-service/internal/service"
)

// AuthHandler serves the auth, session/device, and profile endpoints.
type AuthHandler struct {
	svc      *service.AuthService
	sessions *service.SessionService
	// cookieSecure mirrors auth.cookie-secure; see setRefreshCookie.
	cookieSecure bool
}

func NewAuthHandler(
	svc *service.AuthService, sessions *service.SessionService, cookieSecure bool,
) *AuthHandler {
	return &AuthHandler{svc: svc, sessions: sessions, cookieSecure: cookieSecure}
}

var otpCodeRE = regexp.MustCompile(`^\d{4,8}$`)

// ---- POST /api/auth/signup ----------------------------------------------

type signupReq struct {
	Email    string `json:"email"    example:"user@example.com"`
	Password string `json:"password" example:"hunter2pass"`
}

// Signup godoc
//
// @Summary      Sign up with email + password
// @Description  Creates a PENDING account with an unverified password identity and emails a verification OTP. Re-signing up for an unverified email updates the password and re-issues the OTP.
// @Tags         auth
// @Accept       json
// @Produce      json
// @Param        request  body      signupReq  true  "Signup credentials"
// @Success      201      {object}  service.SignupResult
// @Failure      400      {object}  apperr.ErrorBody  "Validation failed"
// @Failure      409      {object}  apperr.ErrorBody  "Email already registered"
// @Router       /auth/signup [post]
func (h *AuthHandler) Signup(c *fiber.Ctx) error {
	var req signupReq
	if err := c.BodyParser(&req); err != nil {
		return apperr.NewValidation("invalid JSON body")
	}
	req.Email = strings.TrimSpace(req.Email)
	if !looksLikeEmail(req.Email) {
		return apperr.NewValidationWith("Validation failed",
			map[string]string{"email": "email must be valid"})
	}
	res, err := h.svc.Signup(c.Context(), req.Email, req.Password)
	if err != nil {
		return err
	}
	return c.Status(fiber.StatusCreated).JSON(res)
}

// ---- POST /api/auth/verify-email ----------------------------------------

type verifyEmailReq struct {
	Email string `json:"email" example:"user@example.com"`
	OTP   string `json:"otp"   example:"123456"`
}

// VerifyEmail godoc
//
// @Summary      Verify email with OTP
// @Description  Checks the signup OTP; on success verifies the identity, activates the account, and opens a session for the calling device. Token delivery follows the same rules as POST /auth/login.
// @Tags         auth
// @Accept       json
// @Produce      json
// @Param        X-Device-Id        header  string  false  "Stable per-device UUID"
// @Param        X-Device-Name      header  string  false  "Human-readable device name shown in the device list"
// @Param        X-Device-Platform  header  string  false  "ios | android | web"
// @Param        X-Device-Info      header  string  false  "JSON device description, e.g. {\"manufacturer\":\"Apple\",\"model\":\"iPhone15,3\",\"osVersion\":\"17.4\"}"
// @Param        request  body      verifyEmailReq  true  "Email + OTP"
// @Success      200      {object}  service.AuthResult
// @Failure      400      {object}  apperr.ErrorBody  "Wrong / expired / locked OTP"
// @Failure      409      {object}  apperr.ErrorBody  "Email already registered"
// @Router       /auth/verify-email [post]
func (h *AuthHandler) VerifyEmail(c *fiber.Ctx) error {
	var req verifyEmailReq
	if err := c.BodyParser(&req); err != nil {
		return apperr.NewValidation("invalid JSON body")
	}
	req.Email = strings.TrimSpace(req.Email)
	req.OTP = strings.TrimSpace(req.OTP)

	var details map[string]string
	if !looksLikeEmail(req.Email) {
		details = setDetail(details, "email", "email must be valid")
	}
	if !otpCodeRE.MatchString(req.OTP) {
		details = setDetail(details, "otp", "otp must be 4-8 digits")
	}
	if len(details) > 0 {
		return apperr.NewValidationWith("Validation failed", details)
	}

	res, err := h.svc.VerifyEmail(c.Context(), req.Email, req.OTP, middleware.Device(c))
	if err != nil {
		return err
	}
	return h.respondAuth(c, res, middleware.Device(c).IsWeb())
}

// ---- POST /api/auth/otp/resend ------------------------------------------

type resendReq struct {
	Email string `json:"email" example:"user@example.com"`
}

// ResendOTP godoc
//
// @Summary      Resend signup verification OTP
// @Description  Re-issues the signup OTP for an unverified email, subject to cooldown / send limits.
// @Tags         auth
// @Accept       json
// @Produce      json
// @Param        request  body      resendReq        true  "Email to resend to"
// @Success      200      {object}  map[string]string  "{\"message\": \"Verification code re-sent\"}"
// @Failure      400      {object}  apperr.ErrorBody  "Validation failed / cooldown / send limit"
// @Failure      404      {object}  apperr.ErrorBody  "No signup found for this email"
// @Failure      409      {object}  apperr.ErrorBody  "Email is already verified"
// @Router       /auth/otp/resend [post]
func (h *AuthHandler) ResendOTP(c *fiber.Ctx) error {
	var req resendReq
	if err := c.BodyParser(&req); err != nil {
		return apperr.NewValidation("invalid JSON body")
	}
	req.Email = strings.TrimSpace(req.Email)
	if !looksLikeEmail(req.Email) {
		return apperr.NewValidationWith("Validation failed",
			map[string]string{"email": "email must be valid"})
	}
	if err := h.svc.ResendOTP(c.Context(), req.Email); err != nil {
		return err
	}
	return c.JSON(fiber.Map{"message": "Verification code re-sent"})
}

// ---- POST /api/auth/login -----------------------------------------------

type loginReq struct {
	Email    string `json:"email"    example:"user@example.com"`
	Password string `json:"password" example:"hunter2pass"`
}

// Login godoc
//
// @Summary      Log in with email + password
// @Description  Verifies email + password and opens a session for the calling device. Returns a short-lived access JWT plus a refresh token — in the JSON body for mobile clients, or as an httpOnly `refresh_token` cookie when `X-Device-Platform: web`. Send `X-Device-Id` (a stable per-device UUID) so repeat logins update one entry in the device list instead of creating a new one.
// @Tags         auth
// @Accept       json
// @Produce      json
// @Param        X-Device-Id        header  string  false  "Stable per-device UUID"
// @Param        X-Device-Name      header  string  false  "Human-readable device name shown in the device list"
// @Param        X-Device-Platform  header  string  false  "ios | android | web"
// @Param        X-Device-Info      header  string  false  "JSON device description, e.g. {\"manufacturer\":\"Apple\",\"model\":\"iPhone15,3\",\"osVersion\":\"17.4\"}"
// @Param        request  body      loginReq  true  "Login credentials"
// @Success      200      {object}  service.AuthResult
// @Failure      400      {object}  apperr.ErrorBody  "Validation failed"
// @Failure      401      {object}  apperr.ErrorBody  "Invalid email or password / email not verified"
// @Router       /auth/login [post]
func (h *AuthHandler) Login(c *fiber.Ctx) error {
	var req loginReq
	if err := c.BodyParser(&req); err != nil {
		return apperr.NewValidation("invalid JSON body")
	}
	req.Email = strings.TrimSpace(req.Email)
	if req.Email == "" || req.Password == "" {
		return apperr.NewValidationWith("Validation failed",
			map[string]string{"email": "email and password are required"})
	}
	res, err := h.svc.Login(c.Context(), req.Email, req.Password, middleware.Device(c))
	if err != nil {
		return err
	}
	return h.respondAuth(c, res, middleware.Device(c).IsWeb())
}

// ---- POST /api/auth/google -----------------------------------------------

type googleLoginReq struct {
	IDToken string `json:"idToken" example:"eyJhbGciOiJSUzI1NiIs..."`
}

// GoogleLogin godoc
//
// @Summary      Log in with Google
// @Description  Verifies a Google ID token (issued by the Android/iOS Google Sign-In SDK) and returns a session JWT. On first login, creates a verified account; on subsequent logins, reuses the existing account. If the Google email matches an existing account, the Google identity is linked onto it. Requires the Web OAuth client ID to be configured (AUTH_GOOGLE_CLIENT_ID); otherwise returns 503.
// @Tags         auth
// @Accept       json
// @Produce      json
// @Param        X-Device-Id        header  string  false  "Stable per-device UUID"
// @Param        X-Device-Name      header  string  false  "Human-readable device name shown in the device list"
// @Param        X-Device-Platform  header  string  false  "ios | android | web"
// @Param        X-Device-Info      header  string  false  "JSON device description, e.g. {\"manufacturer\":\"Apple\",\"model\":\"iPhone15,3\",\"osVersion\":\"17.4\"}"
// @Param        request  body      googleLoginReq  true  "Google ID token"
// @Success      200      {object}  service.AuthResult
// @Failure      400      {object}  apperr.ErrorBody  "Validation failed (missing idToken)"
// @Failure      401      {object}  apperr.ErrorBody  "Invalid Google token"
// @Failure      409      {object}  apperr.ErrorBody  "Google identity already linked to another account"
// @Failure      503      {object}  apperr.ErrorBody  "Google login is not configured"
// @Router       /auth/google [post]
func (h *AuthHandler) GoogleLogin(c *fiber.Ctx) error {
	var req googleLoginReq
	if err := c.BodyParser(&req); err != nil {
		return apperr.NewValidation("invalid JSON body")
	}
	res, err := h.svc.GoogleLogin(c.Context(), req.IDToken, middleware.Device(c))
	if err != nil {
		return err
	}
	return h.respondAuth(c, res, middleware.Device(c).IsWeb())
}

// ---- GET /api/profile ----------------------------------------------------

// GetProfile godoc
//
// @Summary      Get the authenticated account's profile
// @Description  Returns the full account record for the current session.
// @Tags         profile
// @Produce      json
// @Security     BearerAuth
// @Success      200  {object}  models.Account
// @Failure      401  {object}  apperr.ErrorBody  "Not authenticated"
// @Failure      404  {object}  apperr.ErrorBody  "Account not found"
// @Router       /profile [get]
func (h *AuthHandler) GetProfile(c *fiber.Ctx) error {
	accountID, ok := middleware.AccountID(c)
	if !ok {
		return apperr.NewUnauthorized("Not authenticated")
	}
	acc, err := h.svc.GetAccount(c.Context(), accountID)
	if err != nil {
		return err
	}
	return c.JSON(acc)
}

// ---- PUT /api/profile ----------------------------------------------------

type updateProfileReq struct {
	FirstName   string `json:"firstName"   example:"Ada"`
	LastName    string `json:"lastName"    example:"Lovelace"`
	DateOfBirth string `json:"dateOfBirth" example:"1815-12-10"`
}

// UpdateProfile godoc
//
// @Summary      Update the authenticated account's profile
// @Description  Sets first/last name (required) and optional date of birth (YYYY-MM-DD), marking the profile step complete.
// @Tags         profile
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        request  body      updateProfileReq  true  "Profile fields"
// @Success      200      {object}  models.Account
// @Failure      400      {object}  apperr.ErrorBody  "Validation failed"
// @Failure      401      {object}  apperr.ErrorBody  "Not authenticated"
// @Failure      404      {object}  apperr.ErrorBody  "Account not found"
// @Router       /profile [put]
func (h *AuthHandler) UpdateProfile(c *fiber.Ctx) error {
	accountID, ok := middleware.AccountID(c)
	if !ok {
		return apperr.NewUnauthorized("Not authenticated")
	}
	var req updateProfileReq
	if err := c.BodyParser(&req); err != nil {
		return apperr.NewValidation("invalid JSON body")
	}
	req.FirstName = strings.TrimSpace(req.FirstName)
	req.LastName = strings.TrimSpace(req.LastName)

	var details map[string]string
	if req.FirstName == "" {
		details = setDetail(details, "firstName", "firstName is required")
	}
	if req.LastName == "" {
		details = setDetail(details, "lastName", "lastName is required")
	}

	var dob *time.Time
	if v := strings.TrimSpace(req.DateOfBirth); v != "" {
		t, err := time.Parse("2006-01-02", v)
		if err != nil {
			details = setDetail(details, "dateOfBirth", "dateOfBirth must be YYYY-MM-DD")
		} else {
			utc := t.UTC()
			dob = &utc
		}
	}
	if len(details) > 0 {
		return apperr.NewValidationWith("Validation failed", details)
	}

	acc, err := h.svc.UpdateProfile(c.Context(), accountID, req.FirstName, req.LastName, dob)
	if err != nil {
		return err
	}
	return c.JSON(acc)
}

// ---- PUT /api/admin/accounts/:accountId/role -----------------------------

type setRoleReq struct {
	Role string `json:"role" example:"agent"`
}

// SetAccountRole godoc
//
// @Summary      Set an account's role
// @Description  Grants or revokes a role. 'agent' lets the account issue coupon codes; 'user' is the default. Note that the role rides in the JWT, so a change only takes effect for the target account on its next token refresh — up to one access-token lifetime later.
// @Tags         admin
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        accountId  path      int         true  "Account to modify"
// @Param        request    body      setRoleReq  true  "Role to set (user | agent | admin)"
// @Success      200        {object}  models.Account
// @Failure      400        {object}  apperr.ErrorBody  "Unknown role / accountId must be an integer"
// @Failure      401        {object}  apperr.ErrorBody  "Not authenticated"
// @Failure      403        {object}  apperr.ErrorBody  "Missing the 'account:set-role' permission"
// @Failure      404        {object}  apperr.ErrorBody  "Account not found"
// @Router       /admin/accounts/{accountId}/role [put]
func (h *AuthHandler) SetAccountRole(c *fiber.Ctx) error {
	accountID, err := strconv.ParseInt(c.Params("accountId"), 10, 64)
	if err != nil {
		return apperr.NewValidation("accountId must be an integer")
	}
	var req setRoleReq
	if err := c.BodyParser(&req); err != nil {
		return apperr.NewValidation("invalid JSON body")
	}
	acc, err := h.svc.SetRole(c.Context(), accountID, strings.TrimSpace(req.Role))
	if err != nil {
		return err
	}
	return c.JSON(acc)
}

// ---- shared helpers ------------------------------------------------------

// looksLikeEmail is a deliberately simple check (presence + exactly one @).
func looksLikeEmail(s string) bool {
	at := strings.IndexByte(s, '@')
	if at <= 0 || at == len(s)-1 {
		return false
	}
	return strings.IndexByte(s[at+1:], '@') == -1
}

func setDetail(m map[string]string, k, v string) map[string]string {
	if m == nil {
		m = make(map[string]string)
	}
	m[k] = v
	return m
}
