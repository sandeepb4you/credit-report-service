package repository

import (
	"context"
	"errors"
	"time"

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
    first_name, last_name, date_of_birth, profile_completed,
    referred_by_account_id, referred_by_code, referred_at, token_epoch,
    created_at, updated_at`

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

// FindByPhone looks an account up by its canonical primary phone ("+91…").
func (r *AccountRepo) FindByPhone(ctx context.Context, phone string) (*models.Account, error) {
	var a models.Account
	err := pgxscan.Get(ctx, r.pool, &a,
		`SELECT `+accountCols+` FROM accounts WHERE primary_phone = $1`, phone)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &a, err
}

// SetRole writes the account's role and invalidates its outstanding access
// tokens by bumping token_epoch in the same statement — the two must move
// together, or a token minted between them would carry the new epoch with the
// old role. Returns the new epoch so the caller can mint a fresh token.
func (r *AccountRepo) SetRole(ctx context.Context, accountID int64, role string) (int, error) {
	var epoch int
	err := r.pool.QueryRow(ctx,
		`UPDATE accounts
		    SET role        = $2,
		        token_epoch = token_epoch + 1,
		        updated_at  = now()
		  WHERE id = $1
		 RETURNING token_epoch`,
		accountID, role).Scan(&epoch)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrNotFound
	}
	return epoch, err
}

// TokenEpoch reads the account's current token epoch. This is the per-request
// lookup behind the permission gates, so it selects a single indexed column
// rather than the whole row.
func (r *AccountRepo) TokenEpoch(ctx context.Context, accountID int64) (int, error) {
	var epoch int
	err := r.pool.QueryRow(ctx,
		`SELECT token_epoch FROM accounts WHERE id = $1`, accountID).Scan(&epoch)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrNotFound
	}
	return epoch, err
}

// CreateAccount inserts a new account within a transaction.
//
// Referral attribution is written here and nowhere else: it is a fact about
// how the account came to exist, so it is set once at creation and there is
// deliberately no update path for it.
func (r *AccountRepo) CreateAccount(ctx context.Context, tx pgx.Tx, a *models.Account) error {
	row := tx.QueryRow(ctx,
		`INSERT INTO accounts
		     (status, primary_email, primary_phone,
		      referred_by_account_id, referred_by_code, referred_at)
		 VALUES (COALESCE($1, 'PENDING'), $2, $3, $4, $5,
		         CASE WHEN $4::bigint IS NULL THEN NULL ELSE now() END)
		 RETURNING id, status, profile_completed, referred_at, created_at, updated_at`,
		nilString(a.Status), a.PrimaryEmail, a.PrimaryPhone,
		a.ReferredByAccountID, a.ReferredByCode,
	)
	if err := row.Scan(&a.ID, &a.Status, &a.ProfileCompleted,
		&a.ReferredAt, &a.CreatedAt, &a.UpdatedAt); err != nil {
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

// ---- password_reset_tokens ----------------------------------------------

const passwordResetCols = `id, account_id, token_hash, expires_at, consumed_at, created_at`

// CreatePasswordResetToken stores the digest of a freshly minted reset grant.
func (r *AccountRepo) CreatePasswordResetToken(
	ctx context.Context, tx pgx.Tx, accountID int64, tokenHash string, expiresAt time.Time,
) (*models.PasswordResetToken, error) {
	var t models.PasswordResetToken
	row := tx.QueryRow(ctx,
		`INSERT INTO password_reset_tokens (account_id, token_hash, expires_at)
		 VALUES ($1, $2, $3)
		 RETURNING `+passwordResetCols,
		accountID, tokenHash, expiresAt)
	if err := row.Scan(&t.ID, &t.AccountID, &t.TokenHash,
		&t.ExpiresAt, &t.ConsumedAt, &t.CreatedAt); err != nil {
		return nil, classifyPgErr(err)
	}
	return &t, nil
}

// FindLivePasswordResetToken returns the unconsumed, unexpired grant holding
// this digest, or ErrNotFound. Callers must not distinguish the two cases to
// the client: "wrong token", "already used" and "expired" all mean the same
// thing to the user — start over.
func (r *AccountRepo) FindLivePasswordResetToken(
	ctx context.Context, tokenHash string,
) (*models.PasswordResetToken, error) {
	var t models.PasswordResetToken
	err := pgxscan.Get(ctx, r.pool, &t,
		`SELECT `+passwordResetCols+` FROM password_reset_tokens
		  WHERE token_hash = $1 AND consumed_at IS NULL AND expires_at > now()`,
		tokenHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &t, err
}

// ConsumePasswordResetToken burns a grant. The compare-and-set on consumed_at
// makes redemption single-use under concurrency: only the first caller sees a
// row affected, the loser gets ErrNotFound instead of a second password change.
func (r *AccountRepo) ConsumePasswordResetToken(ctx context.Context, tx pgx.Tx, id int64) error {
	tag, err := tx.Exec(ctx,
		`UPDATE password_reset_tokens SET consumed_at = now()
		  WHERE id = $1 AND consumed_at IS NULL`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// InvalidatePasswordResetTokens burns every outstanding grant for an account,
// so only the newest one is ever redeemable. Called both when issuing a new
// grant and after a successful reset — a password change must not leave an
// older grant alive to change it again.
func (r *AccountRepo) InvalidatePasswordResetTokens(
	ctx context.Context, tx pgx.Tx, accountID int64,
) error {
	_, err := tx.Exec(ctx,
		`UPDATE password_reset_tokens SET consumed_at = now()
		  WHERE account_id = $1 AND consumed_at IS NULL`, accountID)
	return err
}

// ---- kyc_records --------------------------------------------------------

const kycCols = `id, account_id, pan_number, pan_name, pan_verified,
    aadhaar_last4, aadhaar_reference, aadhaar_pan_linked, status, provider,
    rejection_reason, verified_at, reviewed_by_account_id, reviewed_at,
    created_at, updated_at`

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
		        rejection_reason  = NULL,
		        verified_at       = NULL,
		        reviewed_by_account_id = NULL,
		        reviewed_at       = NULL,
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

// ListKYCByStatus returns the KYC review queue for one status, newest activity
// first, joined to the submitting account. Ordering is on updated_at so a
// re-submitted PAN — which needs reviewing again — sorts back to the top;
// account_id breaks ties so paging is stable when timestamps collide.
//
// limit/offset are applied verbatim; the service clamps them.
func (r *AccountRepo) ListKYCByStatus(
	ctx context.Context, status string, limit, offset int,
) ([]models.KYCReviewItem, error) {
	out := []models.KYCReviewItem{}
	err := pgxscan.Select(ctx, r.pool, &out,
		`SELECT k.account_id, a.primary_email, a.primary_phone,
		        a.first_name, a.last_name,
		        k.pan_number, k.pan_name, k.status, k.created_at, k.updated_at
		   FROM kyc_records k
		   JOIN accounts a ON a.id = k.account_id
		  WHERE k.status = $1
		  ORDER BY k.updated_at DESC, k.account_id DESC
		  LIMIT $2 OFFSET $3`,
		status, limit, offset)
	if err != nil {
		return nil, err
	}
	// A page past the end scans to nil; callers serialize [] not null.
	if out == nil {
		out = []models.KYCReviewItem{}
	}
	return out, nil
}

// CountKYCByStatus returns how many KYC rows sit in one status, so a paged
// review queue can show its true size rather than "at least a page".
func (r *AccountRepo) CountKYCByStatus(ctx context.Context, status string) (int, error) {
	var n int
	err := r.pool.QueryRow(ctx,
		`SELECT count(*) FROM kyc_records WHERE status = $1`, status).Scan(&n)
	return n, err
}

// VerifyPAN marks the account's KYC row as PAN-verified. Returns the updated
// row, or ErrNotFound if the account has no KYC row to verify.
func (r *AccountRepo) VerifyPAN(ctx context.Context, accountID, reviewerID int64) (*models.KYCRecord, error) {
	var k models.KYCRecord
	err := pgxscan.Get(ctx, r.pool, &k,
		`UPDATE kyc_records
		    SET pan_verified     = true,
		        status           = 'VERIFIED',
		        rejection_reason = NULL,
		        verified_at      = now(),
		        reviewed_by_account_id = $2,
		        reviewed_at      = now(),
		        updated_at       = now()
		  WHERE account_id = $1
		 RETURNING `+kycCols,
		accountID, nilInt64(reviewerID),
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &k, nil
}

// RejectPAN marks the account's KYC row as rejected, recording why. Returns
// the updated row, or ErrNotFound if the account has no KYC row.
//
// verified_at is cleared and pan_verified forced false, so rejecting a row
// that was previously verified genuinely withdraws access rather than leaving
// a REJECTED row that still satisfies the credit-analytics gate.
func (r *AccountRepo) RejectPAN(ctx context.Context, accountID int64, reason string, reviewerID int64) (*models.KYCRecord, error) {
	var k models.KYCRecord
	err := pgxscan.Get(ctx, r.pool, &k,
		`UPDATE kyc_records
		    SET pan_verified     = false,
		        status           = 'REJECTED',
		        rejection_reason = $2,
		        verified_at      = NULL,
		        reviewed_by_account_id = $3,
		        reviewed_at      = now(),
		        updated_at       = now()
		  WHERE account_id = $1
		 RETURNING `+kycCols,
		accountID, reason, nilInt64(reviewerID),
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &k, nil
}
