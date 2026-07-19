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

const accountCols = `id, status, primary_email, primary_phone,
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
