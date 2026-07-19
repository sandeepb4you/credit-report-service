package models

import "time"

// RegistrationStatus values. Stored as VARCHAR in the DB; the string form
// matches the Java enum names so historical rows remain valid.
const (
	StatusStarted     = "STARTED"
	StatusOTPVerified = "OTP_VERIFIED"
	StatusPANVerified = "PAN_VERIFIED"
	StatusExpired     = "EXPIRED"
)

// RegistrationAttempt is the row model for the registration_attempts table.
// Pointer fields map to nullable columns so we can distinguish "unset" from
// the zero value when reading existing rows.
type RegistrationAttempt struct {
	ID            int64      `json:"id"             db:"id"`
	Mobile        string     `json:"mobile"         db:"mobile"`
	Email         string     `json:"email"          db:"email"`
	Status        string     `json:"status"         db:"status"`

	OTPHash       *string    `json:"-"              db:"otp_hash"`
	OTPExpiresAt  *time.Time `json:"-"              db:"otp_expires_at"`
	OTPAttempts   int        `json:"-"              db:"otp_attempts"`
	OTPSendCount  int        `json:"-"              db:"otp_send_count"`
	LastOTPSentAt *time.Time `json:"-"              db:"last_otp_sent_at"`

	PANNumber     *string    `json:"panNumber"      db:"pan_number"`
	FirstName     *string    `json:"firstName"      db:"first_name"`
	LastName      *string    `json:"lastName"       db:"last_name"`
	DateOfBirth   *time.Time `json:"dateOfBirth"    db:"date_of_birth"`
	PANImagePath  *string    `json:"panImagePath"   db:"pan_image_path"`

	OcrPANNumber  *string    `json:"ocrPanNumber"   db:"ocr_pan_number"`
	OcrPANName    *string    `json:"ocrPanName"     db:"ocr_pan_name"`

	UserID        *int64     `json:"userId"         db:"user_id"`
	CreatedAt     time.Time  `json:"createdAt"      db:"created_at"`
	UpdatedAt     time.Time  `json:"updatedAt"      db:"updated_at"`
}
