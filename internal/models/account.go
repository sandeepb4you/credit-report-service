package models

import (
	"encoding/json"
	"strings"
	"time"
)

// Account lifecycle status.
const (
	AccountPending   = "PENDING" // created, no verified contact yet
	AccountActive    = "ACTIVE"  // has at least one verified identity
	AccountSuspended = "SUSPENDED"
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

	// OtpPurposeAddIdentity verifies a contact point for an account that already
	// exists and is already signed in — today, the mandatory mobile number on an
	// email signup. Deliberately not OtpPurposeLogin: a login challenge is
	// find-or-create and keyed on the number alone, so redeeming one signs the
	// caller into whichever account owns that number. These challenges are bound
	// to the account that requested them and can only ever add to it.
	OtpPurposeAddIdentity = "add_identity"
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

	// Referral attribution, set once at signup and never changed.
	ReferredByAccountID *int64     `json:"referredByAccountId,omitempty" db:"referred_by_account_id"`
	ReferredByCode      *string    `json:"referredByCode,omitempty"      db:"referred_by_code"`
	ReferredAt          *time.Time `json:"referredAt,omitempty"          db:"referred_at"`

	// TokenEpoch is stamped into every access token this account is issued and
	// bumped whenever its role changes. A token carrying an older epoch is
	// refused on the permission-gated routes, so a role change takes effect on
	// the next refresh instead of at token expiry. Not serialized: it is an
	// internal invalidation counter, of no use to a client.
	TokenEpoch int `json:"-" db:"token_epoch"`

	CreatedAt time.Time `json:"createdAt" db:"created_at"`
	UpdatedAt time.Time `json:"updatedAt" db:"updated_at"`
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

	// KycNotSubmitted is reported for an account with no kyc_records row at
	// all. It is never stored — the column only ever holds PENDING/VERIFIED/
	// REJECTED — but the API reports it so a client can drive the onboarding
	// step off one field instead of special-casing "no record yet".
	KycNotSubmitted = "NOT_SUBMITTED"
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
	// ProviderRef is the verification provider's own id for the lookup that
	// decided this record — the handle their support needs to trace it. Not
	// serialized to clients: it is an operational detail, of no use in the app.
	ProviderRef *string `json:"-" db:"provider_ref"`
	// VerificationAttempts counts failed automated checks since the PAN last
	// changed, and is what the retry cap is enforced against. Not serialized:
	// telling a client how many guesses remain helps only an attacker.
	VerificationAttempts int `json:"-" db:"verification_attempts"`
	RejectionReason  *string    `json:"rejectionReason,omitempty" db:"rejection_reason"`
	VerifiedAt       *time.Time `json:"verifiedAt,omitempty" db:"verified_at"`
	// ReviewedByAccountID / ReviewedAt record which admin made the last
	// verify-or-reject decision and when. Both are NULL until someone reviews
	// the submission, and are cleared again if the applicant re-submits.
	ReviewedByAccountID *int64     `json:"reviewedByAccountId,omitempty" db:"reviewed_by_account_id"`
	ReviewedAt          *time.Time `json:"reviewedAt,omitempty"          db:"reviewed_at"`
	CreatedAt           time.Time  `json:"createdAt"         db:"created_at"`
	UpdatedAt           time.Time  `json:"updatedAt"         db:"updated_at"`
}

// KYCStatus is the client-facing projection of an account's KYC state. It
// exists so the API can answer "is KYC done?" without handing back the row —
// KYCRecord carries the full PAN, which is PII the client has no reason to
// receive on a status poll.
type KYCStatus struct {
	// Status is one of KycNotSubmitted / KycPending / KycVerified / KycRejected.
	Status       string `json:"status"       example:"PENDING"`
	PANSubmitted bool   `json:"panSubmitted"`
	PANVerified  bool   `json:"panVerified"`
	// PANLast4 is the tail of the PAN on file, enough for the user to recognise
	// which number they submitted. Empty when nothing is on file.
	PANLast4 string `json:"panLast4,omitempty"   example:"234F"`
	// RejectionReason is set only on a REJECTED record; it is what the client
	// shows the user so they know what to correct before re-submitting.
	RejectionReason *string    `json:"rejectionReason,omitempty"`
	VerifiedAt      *time.Time `json:"verifiedAt,omitempty"`
	// CreatedAt is when the account first submitted a PAN; UpdatedAt is when
	// the record last changed (a re-submission or a verification).
	CreatedAt *time.Time `json:"createdAt,omitempty"`
	UpdatedAt *time.Time `json:"updatedAt,omitempty"`
}

// NewKYCStatus projects a kyc_records row onto the client-facing view. A nil
// record — the usual state for a fresh account — yields KycNotSubmitted rather
// than an error, so every caller has a status to render.
func NewKYCStatus(rec *KYCRecord) KYCStatus {
	if rec == nil {
		return KYCStatus{Status: KycNotSubmitted}
	}
	pan := strings.TrimSpace(rec.PANNumber)
	if pan == "" {
		// pan_number is NOT NULL, so this only happens if a row was written
		// outside SubmitPAN. Report it as nothing on file rather than as a
		// submitted-but-blank PAN.
		return KYCStatus{Status: KycNotSubmitted}
	}
	last4 := pan
	if n := len(last4); n > 4 {
		last4 = last4[n-4:]
	}
	return KYCStatus{
		Status:          rec.Status,
		PANSubmitted:    true,
		PANVerified:     rec.PANVerified,
		PANLast4:        last4,
		RejectionReason: rec.RejectionReason,
		VerifiedAt:      rec.VerifiedAt,
		CreatedAt:       &rec.CreatedAt,
		UpdatedAt:       &rec.UpdatedAt,
	}
}

// Done reports whether KYC is complete — the single flag a client needs to
// decide whether the onboarding KYC step is still outstanding. It requires
// both the status and the PAN flag so a half-written row never reads as done.
func (k KYCStatus) Done() bool { return k.Status == KycVerified && k.PANVerified }

// KYCReviewItem is one row of the admin KYC review queue: a kyc_records row
// joined to enough of its account for a reviewer to act on it. Unlike
// KYCStatus this carries the full PAN — the reviewer's job is to check that
// number — so it must only ever be served behind PermKycVerify.
type KYCReviewItem struct {
	AccountID int64   `json:"accountId" db:"account_id"`
	Email     *string `json:"email"     db:"primary_email"`
	Phone     *string `json:"phone"     db:"primary_phone"`
	FirstName *string `json:"firstName" db:"first_name"`
	LastName  *string `json:"lastName"  db:"last_name"`

	PANNumber string  `json:"pan"               db:"pan_number"`
	PANName   *string `json:"panName,omitempty" db:"pan_name"`
	Status    string  `json:"status"            db:"status"`

	// CreatedAt is when the account first submitted a PAN; UpdatedAt is when
	// the record last changed, and is what the queue is ordered by — a
	// re-submitted PAN needs review again, so it belongs back at the top.
	CreatedAt time.Time `json:"createdAt" db:"created_at"`
	UpdatedAt time.Time `json:"updatedAt" db:"updated_at"`
}

// KYCReviewPage is one page of the admin review queue. Total is the full size
// of the queue, not of this page, so a reviewer can see how much work is
// waiting behind the rows they were served.
type KYCReviewPage struct {
	Items  []KYCReviewItem `json:"items"`
	Total  int             `json:"total"`
	Limit  int             `json:"limit"`
	Offset int             `json:"offset"`
}

// Profile is an account plus its KYC state: what GET/PUT /api/profile return.
// Account is embedded, so the JSON keeps every field it had and gains a "kyc"
// object — one call at app start tells a client both who the user is and
// whether they still owe us a PAN.
type Profile struct {
	Account
	KYC KYCStatus `json:"kyc"`
}

// PrefillLookup is one call to the Mobile to Prefill API, kept so a PAN
// verification decision can be explained after the fact.
//
// ResponseRaw holds only the fields the service decodes — name, DOB, PAN,
// official documents — not the whole upstream body. That bound is deliberate:
// enabling an option at Digitap (addresses, alternate numbers, employment)
// would otherwise start depositing that data here with no code change and no
// decision to collect it. Nothing in this struct is served to a client.
type PrefillLookup struct {
	ID          int64           `json:"-" db:"id"`
	AccountID   int64           `json:"-" db:"account_id"`
	RequestID   *string         `json:"-" db:"request_id"`
	ClientRef   *string         `json:"-" db:"client_ref"`
	ResultCode  *int            `json:"-" db:"result_code"`
	Message     *string         `json:"-" db:"message"`
	PANMatched  *bool           `json:"-" db:"pan_matched"`
	NameMatched *bool           `json:"-" db:"name_matched"`
	Verified    bool            `json:"-" db:"verified"`
	ProviderGap bool            `json:"-" db:"provider_gap"`
	ResponseRaw json.RawMessage `json:"-" db:"response_raw"`
	CreatedAt   time.Time       `json:"-" db:"created_at"`
}

// PasswordResetToken is the row model for the password_reset_tokens table: the
// single-use grant minted once a "forgot password" OTP has been verified, and
// redeemed to actually change the password.
//
// TokenHash is a SHA-256 hex digest, never the token itself — the plaintext
// exists only in the response that hands it to the client, so it can never be
// recovered from the database or re-shown.
type PasswordResetToken struct {
	ID         int64      `json:"id"         db:"id"`
	AccountID  int64      `json:"accountId"  db:"account_id"`
	TokenHash  string     `json:"-"          db:"token_hash"`
	ExpiresAt  time.Time  `json:"expiresAt"  db:"expires_at"`
	ConsumedAt *time.Time `json:"-"          db:"consumed_at"`
	CreatedAt  time.Time  `json:"createdAt"  db:"created_at"`
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
