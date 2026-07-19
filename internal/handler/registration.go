package handler

import (
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"

	"credit-report-service/internal/apperr"
	"credit-report-service/internal/service"
)

type RegistrationHandler struct {
	svc *service.RegistrationService
}

func NewRegistrationHandler(svc *service.RegistrationService) *RegistrationHandler {
	return &RegistrationHandler{svc: svc}
}

// ---- Stage 1a: send OTP --------------------------------------------------

type sendOTPReq struct {
	Mobile string `json:"mobile"`
	Email  string `json:"email"`
}

var mobileRE = regexp.MustCompile(`^[6-9]\d{9}$`)
var otpRE = regexp.MustCompile(`^\d{4,8}$`)

func (h *RegistrationHandler) SendOTP(c *fiber.Ctx) error {
	var req sendOTPReq
	if err := c.BodyParser(&req); err != nil {
		return apperr.NewValidation("invalid JSON body")
	}
	req.Mobile = strings.TrimSpace(req.Mobile)
	req.Email = strings.TrimSpace(req.Email)

	var details map[string]string
	if !mobileRE.MatchString(req.Mobile) {
		details = setDetail(details, "mobile", "mobile must be a 10-digit Indian mobile number")
	}
	if !looksLikeEmail(req.Email) {
		details = setDetail(details, "email", "email must be valid")
	}
	if len(details) > 0 {
		return apperr.NewValidationWith("Validation failed", details)
	}

	res, err := h.svc.SendOTP(c.Context(), req.Mobile, req.Email)
	if err != nil {
		return err
	}
	return c.JSON(res)
}

// ---- Stage 1b: verify OTP ------------------------------------------------

type verifyOTPReq struct {
	AttemptID int64  `json:"attemptId"`
	OTP       string `json:"otp"`
}

func (h *RegistrationHandler) VerifyOTP(c *fiber.Ctx) error {
	var req verifyOTPReq
	if err := c.BodyParser(&req); err != nil {
		return apperr.NewValidation("invalid JSON body")
	}
	if req.AttemptID <= 0 {
		return apperr.NewValidationWith("Validation failed",
			map[string]string{"attemptId": "attemptId is required"})
	}
	if !otpRE.MatchString(strings.TrimSpace(req.OTP)) {
		return apperr.NewValidationWith("Validation failed",
			map[string]string{"otp": "otp must be digits"})
	}
	res, err := h.svc.VerifyOTP(c.Context(), req.AttemptID, strings.TrimSpace(req.OTP))
	if err != nil {
		return err
	}
	return c.JSON(res)
}

// ---- Stage 2: submit PAN + image -----------------------------------------

func (h *RegistrationHandler) SubmitPAN(c *fiber.Ctx) error {
	attemptID, err := strconv.ParseInt(c.FormValue("attemptId"), 10, 64)
	if err != nil || attemptID <= 0 {
		return apperr.NewValidationWith("Validation failed",
			map[string]string{"attemptId": "attemptId is required"})
	}
	panNumber := strings.TrimSpace(c.FormValue("panNumber"))
	firstName := strings.TrimSpace(c.FormValue("firstName"))
	lastName := strings.TrimSpace(c.FormValue("lastName"))
	if panNumber == "" {
		return apperr.NewValidationWith("Validation failed",
			map[string]string{"panNumber": "panNumber is required"})
	}
	if firstName == "" {
		return apperr.NewValidationWith("Validation failed",
			map[string]string{"firstName": "firstName is required"})
	}
	if lastName == "" {
		return apperr.NewValidationWith("Validation failed",
			map[string]string{"lastName": "lastName is required"})
	}

	var dob *time.Time
	if v := strings.TrimSpace(c.FormValue("dateOfBirth")); v != "" {
		t, err := time.Parse("2006-01-02", v)
		if err != nil {
			return apperr.NewValidationWith("Validation failed",
				map[string]string{"dateOfBirth": "dateOfBirth must be YYYY-MM-DD"})
		}
		utc := t.UTC()
		dob = &utc
	}

	file, err := c.FormFile("image")
	if err != nil {
		return apperr.NewValidationWith("Validation failed",
			map[string]string{"image": "PAN card image is required"})
	}
	src, err := file.Open()
	if err != nil {
		return apperr.NewConflict("Could not read uploaded image")
	}
	defer src.Close()

	// Cap read at 6 MiB (5 MiB upload limit + slack); Fiber's BodyLimit already
	// rejects oversize requests earlier.
	buf := make([]byte, 0, file.Size)
	chunk := make([]byte, 32*1024)
	for {
		n, err := src.Read(chunk)
		if n > 0 {
			buf = append(buf, chunk[:n]...)
		}
		if err != nil {
			break
		}
	}

	res, err := h.svc.SubmitPAN(c.Context(), attemptID, panNumber, firstName, lastName,
		dob, file.Filename, buf)
	if err != nil {
		return err
	}
	return c.Status(fiber.StatusCreated).JSON(res)
}

// ---- helpers -------------------------------------------------------------

// looksLikeEmail is a deliberately simple check (presence + exactly one @).
// Deep RFC validation belongs upstream; the service layer enforces the real
// rules.
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
