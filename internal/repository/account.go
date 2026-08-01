package repository

import (
	"context"
	"errors"

	"github.com/georgysavva/scany/v2/pgxscan"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"credit-report-service/internal/models"
)

// AccountRepo is the data access layer for accounts, auth_identities, and
// otp_challenges. They share a repo because the auth service almost always
// touches them together inside one transaction.
type AccountRepo struct{ pool *pgxpool.Pool }

func NewAccountRepo(pool *pgxpool.Pool) *AccountRepo { return &AccountRepo{pool: pool} }

// BeginTx starts a transaction so the service layer doesn't import pgxpool.
func (r *AccountRepo) BeginTx(ctx context.Context) (pgx.Tx, error) {
	return r.pool.Begin(ctx)
}

// ---- accounts -----------------------------------------------------------

const accountCols = `id, status, role, primary_email, primary_phone,
    first_name, last_name, date_of_birth, profile_completed, created_at, updated_at`

func (r *AccountRepo) FindByID(ctx context.Context, id int64) (*models.Account, error) {
	var a models.Account
	err := pgxscan.Get(ctx, r.pool, &a,
		`SELECT `+accountCols+` FROM accounts WHERE id = $1`, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &a, err
}

func (r *AccountRepo) FindByEmail(ctx context.Context, email string) (*models.Account, error) {
	var a models.Account
	err := pgxscan.Get(ctx, r.pool, &a,
		`SELECT `+accountCols+` FROM accounts WHERE primary_email = $1`, email)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &a, err
}

// SetRole updates an account's role. Used by the admin-emails allowlist path
// to promote a verified account to 'admin'.
func (r *AccountRepo) SetRole(ctx context.Context, accountID int64, role string) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE accounts SET role = $2, updated_at = now() WHERE id = $1`,
		accountID, role)
	return err
}

// CreateAccount inserts a new account within a transaction.
func (r *AccountRepo) CreateAccount(ctx context.Context, tx pgx.Tx, a *models.Account) error {
	row := tx.QueryRow(ctx,
		`INSERT INTO accounts (status, primary_email, primary_phone)
		 VALUES (COALESCE($1, 'PENDING'), $2, $3)
		 RETURNING id, status, profile_completed, created_at, updated_at`,
		nilString(a.Status), a.PrimaryEmail, a.PrimaryPhone,
	)
	if err := row.Scan(&a.ID, &a.Status, &a.ProfileCompleted, &a.CreatedAt, &a.UpdatedAt); err != nil {
		return classifyPgErr(err)
	}
	return nil
}

// UpdateAccount saves the mutable account columns (status, contacts, profile).
func (r *AccountRepo) UpdateAccount(ctx context.Context, tx pgx.Tx, a *models.Account) error {
	_, err := tx.Exec(ctx,
		`UPDATE accounts SET
		     status = $2,
		     primary_email = $3,
		     primary_phone = $4,
		     first_name = $5,
		     last_name = $6,
		     date_of_birth = $7,
		     profile_completed = $8,
		     updated_at = now()
		 WHERE id = $1`,
		a.ID, a.Status, a.PrimaryEmail, a.PrimaryPhone,
		a.FirstName, a.LastName, a.DateOfBirth, a.ProfileCompleted,
	)
	return classifyPgErr(err)
}

// ---- auth_identities ----------------------------------------------------

const identityCols = `id, account_id, provider, provider_subject, email, phone,
    password_hash, verified, verified_at, created_at, updated_at`

// FindIdentity looks up a single identity by its (provider, subject) key.
func (r *AccountRepo) FindIdentity(ctx context.Context, provider, subject string) (*models.AuthIdentity, error) {
	var id models.AuthIdentity
	err := pgxscan.Get(ctx, r.pool, &id,
		`SELECT `+identityCols+` FROM auth_identities
		 WHERE provider = $1 AND provider_subject = $2`, provider, subject)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &id, err
}

// FindIdentityByEmail looks up any identity matching the given (normalized)
// email, across providers. Used to link a new Google identity onto an existing
// account whose primary email already matches. Returns ErrNotFound if none.
func (r *AccountRepo) FindIdentityByEmail(ctx context.Context, email string) (*models.AuthIdentity, error) {
	var id models.AuthIdentity
	err := pgxscan.Get(ctx, r.pool, &id,
		`SELECT `+identityCols+` FROM auth_identities
		 WHERE email = $1 ORDER BY verified DESC, id ASC LIMIT 1`, email)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &id, err
}

func (r *AccountRepo) CreateIdentity(ctx context.Context, tx pgx.Tx, id *models.AuthIdentity) error {
	row := tx.QueryRow(ctx,
		`INSERT INTO auth_identities
		     (account_id, provider, provider_subject, email, phone, password_hash, verified, verified_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		 RETURNING id, created_at, updated_at`,
		id.AccountID, id.Provider, id.ProviderSubject, id.Email, id.Phone,
		id.PasswordHash, id.Verified, id.VerifiedAt,
	)
	if err := row.Scan(&id.ID, &id.CreatedAt, &id.UpdatedAt); err != nil {
		return classifyPgErr(err)
	}
	return nil
}

func (r *AccountRepo) UpdateIdentity(ctx context.Context, tx pgx.Tx, id *models.AuthIdentity) error {
	_, err := tx.Exec(ctx,
		`UPDATE auth_identities SET
		     email = $2, phone = $3, password_hash = $4,
		     verified = $5, verified_at = $6, updated_at = now()
		 WHERE id = $1`,
		id.ID, id.Email, id.Phone, id.PasswordHash, id.Verified, id.VerifiedAt,
	)
	return classifyPgErr(err)
}

// ---- otp_challenges -----------------------------------------------------

const otpCols = `id, account_id, channel, destination, purpose,
    otp_hash, expires_at, attempts, send_count, last_sent_at, consumed_at, created_at`

// FindActiveChallenge returns the newest not-yet-consumed challenge for a
// destination+purpose, or ErrNotFound.
func (r *AccountRepo) FindActiveChallenge(ctx context.Context, destination, purpose string) (*models.OtpChallenge, error) {
	var c models.OtpChallenge
	err := pgxscan.Get(ctx, r.pool, &c,
		`SELECT `+otpCols+` FROM otp_challenges
		 WHERE destination = $1 AND purpose = $2 AND consumed_at IS NULL
		 ORDER BY created_at DESC, id DESC LIMIT 1`, destination, purpose)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &c, err
}

func (r *AccountRepo) CreateChallenge(ctx context.Context, tx pgx.Tx, c *models.OtpChallenge) error {
	row := tx.QueryRow(ctx,
		`INSERT INTO otp_challenges
		     (account_id, channel, destination, purpose, otp_hash, expires_at,
		      attempts, send_count, last_sent_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		 RETURNING id, created_at`,
		c.AccountID, c.Channel, c.Destination, c.Purpose, c.OTPHash, c.ExpiresAt,
		c.Attempts, c.SendCount, c.LastSentAt,
	)
	return row.Scan(&c.ID, &c.CreatedAt)
}

func (r *AccountRepo) UpdateChallenge(ctx context.Context, tx pgx.Tx, c *models.OtpChallenge) error {
	_, err := tx.Exec(ctx,
		`UPDATE otp_challenges SET
		     otp_hash = $2, expires_at = $3, attempts = $4, send_count = $5,
		     last_sent_at = $6, consumed_at = $7
		 WHERE id = $1`,
		c.ID, c.OTPHash, c.ExpiresAt, c.Attempts, c.SendCount, c.LastSentAt, c.ConsumedAt,
	)
	return err
}

// ---- kyc_records --------------------------------------------------------

const kycCols = `id, account_id, pan_number, pan_name, pan_verified,
    aadhaar_last4, aadhaar_reference, aadhaar_pan_linked, status, provider,
    verified_at, created_at, updated_at`

// UpsertPAN inserts a PENDING kyc_records row for the account, or — if one
// already exists for this account — replaces the PAN and resets verification
// (the new PAN isn't trusted until re-verified). A PAN already claimed by
// another account surfaces as ErrConflict via the UNIQUE(pan_number) index.
func (r *AccountRepo) UpsertPAN(ctx context.Context, accountID int64, panNumber string) (*models.KYCRecord, error) {
	var k models.KYCRecord
	err := pgxscan.Get(ctx, r.pool, &k,
		`INSERT INTO kyc_records (account_id, pan_number, pan_verified, status)
		 VALUES ($1, $2, false, 'PENDING')
		 ON CONFLICT (account_id) DO UPDATE
		    SET pan_number        = EXCLUDED.pan_number,
		        pan_name          = NULL,
		        pan_verified      = false,
		        aadhaar_last4     = NULL,
		        aadhaar_reference = NULL,
		        aadhaar_pan_linked= NULL,
		        status            = 'PENDING',
		        provider          = NULL,
		        verified_at       = NULL,
		        updated_at        = now()
		 RETURNING `+kycCols,
		accountID, panNumber,
	)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, classifyPgErr(err)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &k, nil
}

// FindKYCByAccount returns the account's KYC row, or ErrNotFound if none exists.
func (r *AccountRepo) FindKYCByAccount(ctx context.Context, accountID int64) (*models.KYCRecord, error) {
	var k models.KYCRecord
	err := pgxscan.Get(ctx, r.pool, &k,
		`SELECT `+kycCols+` FROM kyc_records WHERE account_id = $1`, accountID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &k, nil
}

// VerifyPAN marks the account's KYC row as PAN-verified. Returns the updated
// row, or ErrNotFound if the account has no KYC row to verify.
func (r *AccountRepo) VerifyPAN(ctx context.Context, accountID int64) (*models.KYCRecord, error) {
	var k models.KYCRecord
	err := pgxscan.Get(ctx, r.pool, &k,
		`UPDATE kyc_records
		    SET pan_verified = true,
		        status       = 'VERIFIED',
		        verified_at  = now(),
		        updated_at   = now()
		  WHERE account_id = $1
		 RETURNING `+kycCols,
		accountID,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &k, nil
}
