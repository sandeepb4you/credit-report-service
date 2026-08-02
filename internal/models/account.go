package models

import "time"

// Account lifecycle status.
const (
	AccountPending   = "PENDING" // created, no verified contact yet
	AccountActive    = "ACTIVE"  // has at least one verified identity
	AccountSuspended = "SUSPENDED"
)

// Account roles. The role is assigned via the auth.admin-emails config
// allowlist (auto-promoted at verify/login) and rides in the JWT as a claim.
const (
	RoleUser  = "user"
	RoleAdmin = "admin"
)

// Auth identity providers.
const (
	ProviderPassword = "password"
	ProviderGoogle   = "google"
	ProviderPhone    = "phone"
)

// OTP challenge channels and purposes.
const (
	ChannelEmail = "email"
	ChannelSMS   = "sms"

	OtpPurposeSignup = "signup"
	OtpPurposeLogin  = "login"
	OtpPurposeReset  = "reset"
)

// Account is the row model for the accounts table: one per user. Nullable
// columns are pointers so "unset" is distinguishable from the zero value.
type Account struct {
	ID               int64      `json:"id"               db:"id"`
	Status           string     `json:"status"           db:"status"`
	Role             string     `json:"role"             db:"role"`
	PrimaryEmail     *string    `json:"email"            db:"primary_email"`
	PrimaryPhone     *string    `json:"phone"            db:"primary_phone"`
	FirstName        *string    `json:"firstName"        db:"first_name"`
	LastName         *string    `json:"lastName"         db:"last_name"`
	DateOfBirth      *time.Time `json:"dateOfBirth"      db:"date_of_birth"`
	ProfileCompleted bool       `json:"profileCompleted" db:"profile_completed"`
	AgentID          *int64     `json:"agentId,omitempty" db:"agent_id"`
	AgentCodeUpdated bool       `json:"agentCodeUpdated" db:"agent_code_updated"`
	CreatedAt        time.Time  `json:"createdAt"        db:"created_at"`
	UpdatedAt        time.Time  `json:"updatedAt"        db:"updated_at"`
}

// AuthIdentity is the row model for the auth_identities table: one row per way
// an account can authenticate (password / google / phone).
type AuthIdentity struct {
	ID              int64      `json:"id"              db:"id"`
	AccountID       int64      `json:"accountId"       db:"account_id"`
	Provider        string     `json:"provider"        db:"provider"`
	ProviderSubject string     `json:"providerSubject" db:"provider_subject"`
	Email           *string    `json:"email"           db:"email"`
	Phone           *string    `json:"phone"           db:"phone"`
	PasswordHash    *string    `json:"-"               db:"password_hash"`
	Verified        bool       `json:"verified"        db:"verified"`
	VerifiedAt      *time.Time `json:"verifiedAt"      db:"verified_at"`
	CreatedAt       time.Time  `json:"createdAt"       db:"created_at"`
	UpdatedAt       time.Time  `json:"updatedAt"       db:"updated_at"`
}

// KYC lifecycle status, mirroring kyc_records.status.
const (
	KycPending  = "PENDING"  // PAN accepted, awaiting verification
	KycVerified = "VERIFIED" // KYC complete; gates the analysis products
	KycRejected = "REJECTED"
)

// KYCRecord is the row model for the kyc_records table: Aadhaar + PAN
// verification. Only PAN is accepted via the public API; the Aadhaar fields
// are populated by a separate KYC-provider flow. PAN is sensitive PII.
type KYCRecord struct {
	ID               int64      `json:"id"                db:"id"`
	AccountID        int64      `json:"accountId"         db:"account_id"`
	PANNumber        string     `json:"pan"               db:"pan_number"`
	PANName          *string    `json:"panName,omitempty" db:"pan_name"`
	PANVerified      bool       `json:"panVerified"       db:"pan_verified"`
	AadhaarLast4     *string    `json:"aadhaarLast4,omitempty" db:"aadhaar_last4"`
	AadhaarReference *string    `json:"aadhaarReference,omitempty" db:"aadhaar_reference"`
	AadhaarPanLinked *bool      `json:"aadhaarPanLinked,omitempty" db:"aadhaar_pan_linked"`
	Status           string     `json:"status"            db:"status"`
	Provider         *string    `json:"provider,omitempty" db:"provider"`
	VerifiedAt       *time.Time `json:"verifiedAt,omitempty" db:"verified_at"`
	CreatedAt        time.Time  `json:"createdAt"         db:"created_at"`
	UpdatedAt        time.Time  `json:"updatedAt"         db:"updated_at"`
}

// OtpChallenge is the row model for the otp_challenges table: a transient
// one-time-password verification for an email or phone destination.
type OtpChallenge struct {
	ID          int64  `json:"id"          db:"id"`
	AccountID   *int64 `json:"accountId"   db:"account_id"`
	Channel     string `json:"channel"     db:"channel"`
	Destination string `json:"destination" db:"destination"`
	Purpose     string `json:"purpose"     db:"purpose"`

	OTPHash    *string    `json:"-"           db:"otp_hash"`
	ExpiresAt  *time.Time `json:"-"           db:"expires_at"`
	Attempts   int        `json:"-"           db:"attempts"`
	SendCount  int        `json:"-"           db:"send_count"`
	LastSentAt *time.Time `json:"-"           db:"last_sent_at"`
	ConsumedAt *time.Time `json:"-"           db:"consumed_at"`

	CreatedAt time.Time `json:"createdAt"   db:"created_at"`
}
