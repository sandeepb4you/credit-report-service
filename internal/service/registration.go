package service

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"credit-report-service/internal/apperr"
	"credit-report-service/internal/config"
	"credit-report-service/internal/models"
	"credit-report-service/internal/ocr"
	"credit-report-service/internal/repository"
)

// RegistrationService orchestrates the 3-stage registration flow.
//
//	Stage 1a: SendOTP      -> attemptId
//	Stage 1b: VerifyOTP    -> attempt becomes OTP_VERIFIED
//	Stage 2:  SubmitPAN    -> creates a User row, attempt becomes PAN_VERIFIED
type RegistrationService struct {
	attempts  *repository.RegistrationRepo
	users     *repository.UserRepo
	otp       *OTPService
	mailer    Mailer
	ocr       ocr.Provider
	pan       *PanValidator
	cfg       config.RegistrationConfig
}

func NewRegistrationService(
	attempts *repository.RegistrationRepo,
	users *repository.UserRepo,
	otp *OTPService,
	mailer Mailer,
	ocr ocr.Provider,
	pan *PanValidator,
	cfg config.RegistrationConfig,
) *RegistrationService {
	return &RegistrationService{
		attempts: attempts, users: users, otp: otp, mailer: mailer, ocr: ocr, pan: pan, cfg: cfg,
	}
}

// SendOTPResult is returned to the HTTP layer.
type SendOTPResult struct {
	AttemptID              int64     `json:"attemptId"`
	ExpiresAt              time.Time `json:"expiresAt"`
	ResendAvailableInSeconds int      `json:"resendAvailableInSeconds"`
}

// SendOTP creates/resumes an attempt, issues an OTP, and emails it.
func (s *RegistrationService) SendOTP(ctx context.Context, mobile, email string) (*SendOTPResult, error) {
	// Reject duplicates against already-confirmed users up front.
	if exists, err := s.users.ExistsByMobile(ctx, mobile); err != nil {
		return nil, err
	} else if exists {
		return nil, apperr.NewConflict("Mobile number already registered")
	}
	if exists, err := s.users.ExistsByEmail(ctx, email); err != nil {
		return nil, err
	} else if exists {
		return nil, apperr.NewConflict("Email already registered")
	}

	// Reuse the latest in-flight attempt for this mobile, if any; else start fresh.
	var attempt *models.RegistrationAttempt
	if a, err := s.attempts.FindLatestByMobile(ctx, mobile); err == nil && a.Status == models.StatusStarted {
		attempt = a
	} else if err != nil && !errors.Is(err, repository.ErrNotFound) {
		return nil, err
	}
	if attempt == nil {
		attempt = &models.RegistrationAttempt{Mobile: mobile, Email: email, Status: models.StatusStarted}
	} else {
		// Always overwrite email on the resumed attempt.
		attempt.Email = email
	}

	plain, err := s.otp.Issue(attempt)
	if err != nil {
		return nil, err
	}

	// Persist outside a tx for the create path; the only op here is the attempt row.
	if err := s.persistAttempt(ctx, attempt); err != nil {
		return nil, err
	}
	if err := s.mailer.SendOTP(email, plain); err != nil {
		return nil, err
	}

	return &SendOTPResult{
		AttemptID:                attempt.ID,
		ExpiresAt:                derefTime(attempt.OTPExpiresAt),
		ResendAvailableInSeconds: int(s.cfg.OTP.ResendCooldown.Round(time.Second).Seconds()),
	}, nil
}

// VerifyOTPResult is returned to the HTTP layer.
type VerifyOTPResult struct {
	AttemptID int64  `json:"attemptId"`
	Status    string `json:"status"`
}

func (s *RegistrationService) VerifyOTP(ctx context.Context, attemptID int64, supplied string) (*VerifyOTPResult, error) {
	a, err := s.loadAttempt(ctx, attemptID)
	if err != nil {
		return nil, err
	}
	if err := s.otp.Verify(a, supplied); err != nil {
		// Still persist the incremented attempt counter.
		_ = s.persistAttempt(ctx, a)
		return nil, err
	}
	if err := s.persistAttempt(ctx, a); err != nil {
		return nil, err
	}
	return &VerifyOTPResult{AttemptID: a.ID, Status: a.Status}, nil
}

// SubmitPANResult is returned to the HTTP layer.
type SubmitPANResult struct {
	UserID    int64  `json:"userId"`
	AttemptID int64  `json:"attemptId"`
	Status    string `json:"status"`
}

// SubmitPAN runs OCR on the uploaded image, validates the submitted PAN, and
// creates the user row. On validation failure the attempt stays OTP_VERIFIED
// so the client can retry with a better image/fields.
func (s *RegistrationService) SubmitPAN(
	ctx context.Context,
	attemptID int64,
	panNumber, firstName, lastName string,
	dateOfBirth *time.Time,
	imageName string, imageBytes []byte,
) (*SubmitPANResult, error) {
	a, err := s.loadAttempt(ctx, attemptID)
	if err != nil {
		return nil, err
	}
	if a.Status != models.StatusOTPVerified {
		return nil, apperr.NewConflict(
			"PAN submission requires a verified OTP. Current status: " + a.Status)
	}
	if len(imageBytes) == 0 {
		return nil, apperr.NewConflict("PAN card image is required")
	}

	pan := strings.ToUpper(strings.TrimSpace(panNumber))
	if pan != "" {
		if exists, err := s.users.ExistsByPAN(ctx, pan); err != nil {
			return nil, err
		} else if exists {
			return nil, apperr.NewConflict("PAN number already registered")
		}
	}

	imagePath, err := s.persistImage(a.ID, imageName, imageBytes)
	if err != nil {
		return nil, err
	}

	r, err := s.ocr.Extract(imageBytes, contentTypeFromName(imageName))
	if err != nil {
		return nil, apperr.NewConflict("Could not read uploaded image")
	}
	// Throws PanFailure on mismatch — attempt stays OTP_VERIFIED for retry.
	if err := s.pan.Validate(pan, firstName, lastName, r); err != nil {
		return nil, err
	}

	// Persist user + attempt update atomically.
	tx, err := s.attempts.BeginTx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	user := &models.User{
		Mobile:       a.Mobile,
		Email:        a.Email,
		PANNumber:    pan,
		FirstName:    strings.TrimSpace(firstName),
		LastName:     strings.TrimSpace(lastName),
		DateOfBirth:  dateOfBirth,
		PANImagePath: &imagePath,
	}
	if err := s.users.Create(ctx, tx, user); err != nil {
		if errors.Is(err, repository.ErrConflict) {
			return nil, apperr.NewConflict("PAN, mobile, or email already registered")
		}
		return nil, err
	}

	first := strings.TrimSpace(firstName)
	last := strings.TrimSpace(lastName)
	dob := dateOfBirth
	a.PANNumber = &pan
	a.FirstName = &first
	a.LastName = &last
	a.DateOfBirth = dob
	a.PANImagePath = &imagePath
	a.OcrPANNumber = strPtrOrNil(r.PANNumber)
	a.OcrPANName = strPtrOrNil(r.Name)
	a.UserID = &user.ID
	a.Status = models.StatusPANVerified
	if err := s.attempts.Save(ctx, tx, a); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}

	log.Printf("registration complete for mobile=%s userId=%d", a.Mobile, user.ID)
	return &SubmitPANResult{UserID: user.ID, AttemptID: a.ID, Status: a.Status}, nil
}

// ---- helpers -----------------------------------------------------------

func (s *RegistrationService) loadAttempt(ctx context.Context, id int64) (*models.RegistrationAttempt, error) {
	a, err := s.attempts.FindByID(ctx, id)
	if errors.Is(err, repository.ErrNotFound) {
		return nil, apperr.NewConflict(fmt.Sprintf("Registration attempt not found: %d", id))
	}
	return a, err
}

// persistAttempt wraps create-vs-update so callers don't think about it.
func (s *RegistrationService) persistAttempt(ctx context.Context, a *models.RegistrationAttempt) error {
	tx, err := s.attempts.BeginTx(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if a.ID == 0 {
		if err := s.attempts.Create(ctx, tx, a); err != nil {
			return err
		}
	} else {
		if err := s.attempts.Save(ctx, tx, a); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// persistImage writes the uploaded image to disk with the same filename
// convention as the Java service: pan_<attemptId>_<uuid><ext>.
func (s *RegistrationService) persistImage(attemptID int64, originalName string, contents []byte) (string, error) {
	dir := s.cfg.PanImageDir
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create pan image dir: %w", err)
	}
	ext := filepath.Ext(originalName)
	if ext == "" {
		ext = ".jpg"
	}
	filename := fmt.Sprintf("pan_%d_%s%s", attemptID, uuid.NewString(), ext)
	full := filepath.Join(dir, filename)

	if err := os.WriteFile(full, contents, 0o644); err != nil {
		return "", fmt.Errorf("write pan image: %w", err)
	}
	return full, nil
}

func derefTime(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return *t
}

func strPtrOrNil(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func contentTypeFromName(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".png":
		return "image/png"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	default:
		return "image/jpeg"
	}
}
