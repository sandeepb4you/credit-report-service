package repository

import (
	"context"
	"errors"
	"fmt"

	"github.com/georgysavva/scany/v2/pgxscan"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"credit-report-service/internal/models"
)

type RegistrationRepo struct{ pool *pgxpool.Pool }

func NewRegistrationRepo(pool *pgxpool.Pool) *RegistrationRepo {
	return &RegistrationRepo{pool: pool}
}

// BeginTx starts a new transaction from the repo's pool. Service code uses
// this so it doesn't have to import pgxpool directly.
func (r *RegistrationRepo) BeginTx(ctx context.Context) (pgx.Tx, error) {
	return r.pool.Begin(ctx)
}

const regCols = `id, mobile, email, status,
    otp_hash, otp_expires_at, otp_attempts, otp_send_count, last_otp_sent_at,
    pan_number, first_name, last_name, date_of_birth, pan_image_path,
    ocr_pan_number, ocr_pan_name, user_id, created_at, updated_at`

func (r *RegistrationRepo) FindByID(ctx context.Context, id int64) (*models.RegistrationAttempt, error) {
	var a models.RegistrationAttempt
	err := pgxscan.Get(ctx, r.pool, &a,
		fmt.Sprintf(`SELECT %s FROM registration_attempts WHERE id = $1`, regCols), id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &a, err
}

func (r *RegistrationRepo) FindLatestByMobile(ctx context.Context, mobile string) (*models.RegistrationAttempt, error) {
	var a models.RegistrationAttempt
	err := pgxscan.Get(ctx, r.pool, &a,
		fmt.Sprintf(`SELECT %s FROM registration_attempts
		             WHERE mobile = $1
		             ORDER BY created_at DESC, id DESC LIMIT 1`, regCols), mobile)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &a, err
}

// Create inserts a new attempt and returns it with id/timestamps set.
func (r *RegistrationRepo) Create(ctx context.Context, tx pgx.Tx, a *models.RegistrationAttempt) error {
	row := tx.QueryRow(ctx,
		`INSERT INTO registration_attempts (mobile, email, status)
		 VALUES ($1, $2, COALESCE($3, 'STARTED'))
		 RETURNING id, status, created_at, updated_at`,
		a.Mobile, a.Email, nilString(a.Status),
	)
	return row.Scan(&a.ID, &a.Status, &a.CreatedAt, &a.UpdatedAt)
}

// Save performs an UPDATE on all mutable columns.
func (r *RegistrationRepo) Save(ctx context.Context, tx pgx.Tx, a *models.RegistrationAttempt) error {
	_, err := tx.Exec(ctx,
		`UPDATE registration_attempts SET
		     email = $2,
		     status = $3,
		     otp_hash = $4,
		     otp_expires_at = $5,
		     otp_attempts = $6,
		     otp_send_count = $7,
		     last_otp_sent_at = $8,
		     pan_number = $9,
		     first_name = $10,
		     last_name = $11,
		     date_of_birth = $12,
		     pan_image_path = $13,
		     ocr_pan_number = $14,
		     ocr_pan_name = $15,
		     user_id = $16,
		     updated_at = now()
		 WHERE id = $1`,
		a.ID, a.Email, a.Status,
		a.OTPHash, a.OTPExpiresAt, a.OTPAttempts, a.OTPSendCount, a.LastOTPSentAt,
		a.PANNumber, a.FirstName, a.LastName, a.DateOfBirth, a.PANImagePath,
		a.OcrPANNumber, a.OcrPANName, a.UserID,
	)
	return err
}
