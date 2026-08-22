package handler

import (
	"fmt"
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
	// ReferralCode is optional and attributes the new account to whoever owns
	// it. An invalid code fails the signup rather than being ignored.
	ReferralCode string `json:"referralCode" example:"REF-7K2QM4XZ"`
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
	res, err := h.svc.Signup(c.Context(), req.Email, req.Password, req.ReferralCode)
	if err != nil {
		return err
	}
	return c.Status(fiber.StatusCreated).JSON(res)
}

// ---- POST /api/auth/verify-email ----------------------------------------

type verifyEmailReq struct {
	Email string `json:"email" example:"user@example.com"`
	OTP   string `json:"otp"   example:"1234"`
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

// ---- POST /api/auth/otp/phone/send ----------------------------------------

type phoneOtpSendReq struct {
	Phone string `json:"phone" example:"+919876543210"`
}

// SendPhoneOTP godoc
//
// @Summary      Send a phone sign-in OTP
// @Description  Sends a one-time code to an Indian mobile number (bare 10 digits or +91-prefixed). Unknown numbers are allowed — verifying the code creates the account. Subject to the same cooldown / send limits as email OTPs. Delivered by SMS through MSG91 when an auth key is configured; without one the code is written to the server log instead.
// @Tags         auth
// @Accept       json
// @Produce      json
// @Param        request  body      phoneOtpSendReq   true  "Mobile number"
// @Success      200      {object}  map[string]string  "{\"message\": \"Verification code sent\"}"
// @Failure      400      {object}  apperr.ErrorBody  "Validation failed"
// @Failure      409      {object}  apperr.ErrorBody  "Resend cooldown / send limit reached"
// @Router       /auth/otp/phone/send [post]
func (h *AuthHandler) SendPhoneOTP(c *fiber.Ctx) error {
	var req phoneOtpSendReq
	if err := c.BodyParser(&req); err != nil {
		return apperr.NewValidation("invalid JSON body")
	}
	if err := h.svc.SendPhoneOTP(c.Context(), strings.TrimSpace(req.Phone)); err != nil {
		return err
	}
	return c.JSON(fiber.Map{"message": "Verification code sent"})
}

// ---- POST /api/auth/otp/phone/verify ---------------------------------------

type phoneOtpVerifyReq struct {
	Phone string `json:"phone" example:"+919876543210"`
	OTP   string `json:"otp"   example:"1234"`
}

// VerifyPhoneOTP godoc
//
// @Summary      Verify a phone OTP and sign in
// @Description  Checks the code sent by POST /auth/otp/phone/send and opens a session for the calling device. A first-time number gets an account created on the spot (profile incomplete); an existing number signs into its account. Returns the same session payload as email login.
// @Tags         auth
// @Accept       json
// @Produce      json
// @Param        X-Device-Id        header  string  false  "Stable per-device UUID"
// @Param        X-Device-Name      header  string  false  "Human-readable device name shown in the device list"
// @Param        X-Device-Platform  header  string  false  "ios | android | web"
// @Param        X-Device-Info      header  string  false  "JSON device description"
// @Param        request  body      phoneOtpVerifyReq  true  "Phone + OTP"
// @Success      200      {object}  service.AuthResult
// @Failure      400      {object}  apperr.ErrorBody  "Validation failed / wrong / expired / locked OTP"
// @Router       /auth/otp/phone/verify [post]
func (h *AuthHandler) VerifyPhoneOTP(c *fiber.Ctx) error {
	var req phoneOtpVerifyReq
	if err := c.BodyParser(&req); err != nil {
		return apperr.NewValidation("invalid JSON body")
	}
	req.OTP = strings.TrimSpace(req.OTP)
	if !otpCodeRE.MatchString(req.OTP) {
		return apperr.NewValidationWith("Validation failed",
			map[string]string{"otp": "otp must be 4-8 digits"})
	}
	res, err := h.svc.VerifyPhoneOTP(
		c.Context(), strings.TrimSpace(req.Phone), req.OTP, middleware.Device(c))
	if err != nil {
		return err
	}
	return h.respondAuth(c, res, middleware.Device(c).IsWeb())
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

// ---- POST /api/auth/password/forgot --------------------------------------

type forgotPasswordReq struct {
	Email string `json:"email" example:"user@example.com"`
}

// forgotPasswordMsg is returned whether or not the address has an account —
// see AuthService.ForgotPassword. It is phrased so that it is true either way.
const forgotPasswordMsg = "If that email has an account, a reset code is on its way"

// ForgotPassword godoc
//
// @Summary      Start a password reset
// @Description  Emails a one-time code to a verified email+password account. Returns the same 200 for an address with no account, so the endpoint cannot be used to discover which emails are registered. Call again to resend, subject to the same cooldown / send limits as signup.
// @Tags         auth
// @Accept       json
// @Produce      json
// @Param        request  body      forgotPasswordReq  true  "Account email"
// @Success      200      {object}  map[string]string  "{\"message\": \"If that email has an account, a reset code is on its way\"}"
// @Failure      400      {object}  apperr.ErrorBody  "Validation failed"
// @Failure      409      {object}  apperr.ErrorBody  "Resend cooldown / send limit reached"
// @Router       /auth/password/forgot [post]
func (h *AuthHandler) ForgotPassword(c *fiber.Ctx) error {
	var req forgotPasswordReq
	if err := c.BodyParser(&req); err != nil {
		return apperr.NewValidation("invalid JSON body")
	}
	req.Email = strings.TrimSpace(req.Email)
	if !looksLikeEmail(req.Email) {
		return apperr.NewValidationWith("Validation failed",
			map[string]string{"email": "email must be valid"})
	}
	if err := h.svc.ForgotPassword(c.Context(), req.Email); err != nil {
		return err
	}
	return c.JSON(fiber.Map{"message": forgotPasswordMsg})
}

// ---- POST /api/auth/password/verify-otp ----------------------------------

type verifyResetOtpReq struct {
	Email string `json:"email" example:"user@example.com"`
	OTP   string `json:"otp"   example:"1234"`
}

// VerifyPasswordResetOTP godoc
//
// @Summary      Verify a password-reset code
// @Description  Checks the code emailed by POST /auth/password/forgot and returns a single-use `resetToken`, which POST /auth/password/reset redeems to set the new password. The code is consumed here — a wrong code counts against the same attempt limit as signup verification.
// @Tags         auth
// @Accept       json
// @Produce      json
// @Param        request  body      verifyResetOtpReq  true  "Email + OTP"
// @Success      200      {object}  service.PasswordResetGrant
// @Failure      400      {object}  apperr.ErrorBody  "Wrong / expired / locked OTP"
// @Failure      404      {object}  apperr.ErrorBody  "No account found for this email"
// @Router       /auth/password/verify-otp [post]
func (h *AuthHandler) VerifyPasswordResetOTP(c *fiber.Ctx) error {
	var req verifyResetOtpReq
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

	grant, err := h.svc.VerifyPasswordResetOTP(c.Context(), req.Email, req.OTP)
	if err != nil {
		return err
	}
	return c.JSON(grant)
}

// ---- POST /api/auth/password/reset ---------------------------------------

type resetPasswordReq struct {
	Email      string `json:"email"      example:"user@example.com"`
	ResetToken string `json:"resetToken" example:"prt_8Kd2..."`
	Password   string `json:"password"   example:"newhunter2pass"`
}

// ResetPassword godoc
//
// @Summary      Set a new password with a verified reset token
// @Description  Redeems the single-use `resetToken` from POST /auth/password/verify-otp and writes the new password. Every signed-in device is then signed out, so the caller must log in again with the new password. The token is spent whether or not the caller keeps the response.
// @Tags         auth
// @Accept       json
// @Produce      json
// @Param        request  body      resetPasswordReq  true  "Email + reset token + new password"
// @Success      200      {object}  map[string]string  "{\"message\": \"Password updated. Please sign in.\"}"
// @Failure      400      {object}  apperr.ErrorBody  "Validation failed (password too short/long)"
// @Failure      401      {object}  apperr.ErrorBody  "Reset token is unknown, expired or already used"
// @Router       /auth/password/reset [post]
func (h *AuthHandler) ResetPassword(c *fiber.Ctx) error {
	var req resetPasswordReq
	if err := c.BodyParser(&req); err != nil {
		return apperr.NewValidation("invalid JSON body")
	}
	req.Email = strings.TrimSpace(req.Email)
	if !looksLikeEmail(req.Email) {
		return apperr.NewValidationWith("Validation failed",
			map[string]string{"email": "email must be valid"})
	}
	// Password rules live in the service (one place for signup and reset
	// alike); the token is validated by lookup, so nothing else to check here.
	if err := h.svc.ResetPassword(
		c.Context(), req.Email, req.ResetToken, req.Password); err != nil {
		return err
	}
	return c.JSON(fiber.Map{"message": "Password updated. Please sign in."})
}

// ---- POST /api/auth/google -----------------------------------------------

type googleLoginReq struct {
	IDToken string `json:"idToken" example:"eyJhbGciOiJSUzI1NiIs..."`
	// ReferralCode is only honoured when this login creates a new account.
	ReferralCode string `json:"referralCode" example:"REF-7K2QM4XZ"`
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
	res, err := h.svc.GoogleLogin(c.Context(), req.IDToken, req.ReferralCode, middleware.Device(c))
	if err != nil {
		return err
	}
	return h.respondAuth(c, res, middleware.Device(c).IsWeb())
}

// ---- GET /api/profile ----------------------------------------------------

// GetProfile godoc
//
// @Summary      Get the authenticated account's profile
// @Description  Returns the full account record for the current session, plus a "kyc" block carrying the account's KYC state (status, panVerified, last 4 of the PAN). Clients should read KYC completion from here rather than tracking it locally.
// @Tags         profile
// @Produce      json
// @Security     BearerAuth
// @Success      200  {object}  models.Profile
// @Failure      401  {object}  apperr.ErrorBody  "Not authenticated"
// @Failure      404  {object}  apperr.ErrorBody  "Account not found"
// @Router       /profile [get]
func (h *AuthHandler) GetProfile(c *fiber.Ctx) error {
	accountID, ok := middleware.AccountID(c)
	if !ok {
		return apperr.NewUnauthorized("Not authenticated")
	}
	prof, err := h.svc.GetProfile(c.Context(), accountID)
	if err != nil {
		return err
	}
	return c.JSON(prof)
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
// @Success      200      {object}  models.Profile
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
	switch {
	case req.FirstName == "":
		details = setDetail(details, "firstName", "firstName is required")
	case len(req.FirstName) > maxNameLen:
		details = setDetail(details, "firstName",
			fmt.Sprintf("firstName must be at most %d characters", maxNameLen))
	}
	switch {
	case req.LastName == "":
		details = setDetail(details, "lastName", "lastName is required")
	case len(req.LastName) > maxNameLen:
		details = setDetail(details, "lastName",
			fmt.Sprintf("lastName must be at most %d characters", maxNameLen))
	}

	var dob *time.Time
	if v := strings.TrimSpace(req.DateOfBirth); v != "" {
		t, err := time.Parse("2006-01-02", v)
		switch {
		case err != nil:
			details = setDetail(details, "dateOfBirth", "dateOfBirth must be YYYY-MM-DD")
		default:
			utc := t.UTC()
			if msg := validateDOB(utc, time.Now().UTC()); msg != "" {
				details = setDetail(details, "dateOfBirth", msg)
			} else {
				dob = &utc
			}
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

// Length ceilings mirroring the column widths these values land in
// (accounts.first_name / last_name, auth_identities.email and
// provider_subject are all VARCHAR(255)). Postgres rejects an over-long value
// rather than truncating it, so without these checks a long name or address
// becomes a database error instead of a field-level 400.
const (
	maxEmailLen = 255
	maxNameLen  = 255
)

// Date-of-birth bounds. A DOB must be in the past and within a plausible
// lifespan; the ceiling exists because the column is a DATE that will happily
// store year 1000. This deliberately does not enforce a minimum age —
// whether the product requires 18+ is a policy decision, not a parsing one.
const maxAgeYears = 120

// looksLikeEmail is a deliberately simple check (presence + exactly one @,
// within the column width).
func looksLikeEmail(s string) bool {
	if len(s) > maxEmailLen {
		return false
	}
	at := strings.IndexByte(s, '@')
	if at <= 0 || at == len(s)-1 {
		return false
	}
	return strings.IndexByte(s[at+1:], '@') == -1
}

// validateDOB checks a parsed date of birth against the bounds above,
// returning the message for the field or "" when it is acceptable.
func validateDOB(dob, now time.Time) string {
	switch {
	case dob.After(now):
		return "dateOfBirth cannot be in the future"
	case dob.Before(now.AddDate(-maxAgeYears, 0, 0)):
		return fmt.Sprintf("dateOfBirth cannot be more than %d years ago", maxAgeYears)
	}
	return ""
}

func setDetail(m map[string]string, k, v string) map[string]string {
	if m == nil {
		m = make(map[string]string)
	}
	m[k] = v
	return m
}
