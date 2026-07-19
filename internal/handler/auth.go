package handler

import (
	"regexp"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"

	"credit-report-service/internal/apperr"
	"credit-report-service/internal/server/middleware"
	"credit-report-service/internal/service"
)

// AuthHandler serves the email/password + OTP auth and profile endpoints.
type AuthHandler struct {
	svc *service.AuthService
}

func NewAuthHandler(svc *service.AuthService) *AuthHandler {
	return &AuthHandler{svc: svc}
}

var otpCodeRE = regexp.MustCompile(`^\d{4,8}$`)

// ---- POST /api/auth/signup ----------------------------------------------

type signupReq struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

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
	Email string `json:"email"`
	OTP   string `json:"otp"`
}

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

	res, err := h.svc.VerifyEmail(c.Context(), req.Email, req.OTP)
	if err != nil {
		return err
	}
	return c.JSON(res)
}

// ---- POST /api/auth/otp/resend ------------------------------------------

type resendReq struct {
	Email string `json:"email"`
}

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
	Email    string `json:"email"`
	Password string `json:"password"`
}

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
	res, err := h.svc.Login(c.Context(), req.Email, req.Password)
	if err != nil {
		return err
	}
	return c.JSON(res)
}

// ---- GET /api/profile ----------------------------------------------------

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
	FirstName   string `json:"firstName"`
	LastName    string `json:"lastName"`
	DateOfBirth string `json:"dateOfBirth"`
}

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
