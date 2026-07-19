package repository

import (
	"context"
	"errors"

	"github.com/georgysavva/scany/v2/pgxscan"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"credit-report-service/internal/models"
)

type UserRepo struct{ pool *pgxpool.Pool }

func NewUserRepo(pool *pgxpool.Pool) *UserRepo { return &UserRepo{pool: pool} }

func (r *UserRepo) ExistsByMobile(ctx context.Context, mobile string) (bool, error) {
	return r.exists(ctx, `SELECT 1 FROM users WHERE mobile = $1`, mobile)
}

func (r *UserRepo) ExistsByEmail(ctx context.Context, email string) (bool, error) {
	return r.exists(ctx, `SELECT 1 FROM users WHERE email = $1`, email)
}

func (r *UserRepo) ExistsByPAN(ctx context.Context, pan string) (bool, error) {
	return r.exists(ctx, `SELECT 1 FROM users WHERE pan_number = $1`, pan)
}

func (r *UserRepo) FindByMobile(ctx context.Context, mobile string) (*models.User, error) {
	var u models.User
	err := pgxscan.Get(ctx, r.pool, &u,
		`SELECT id, mobile, email, pan_number, first_name, last_name,
		        date_of_birth, pan_image_path, status, created_at, updated_at
		 FROM users
		 WHERE mobile = $1`, mobile)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &u, err
}

func (r *UserRepo) Create(ctx context.Context, tx pgx.Tx, u *models.User) error {
	row := tx.QueryRow(ctx,
		`INSERT INTO users (mobile, email, pan_number, first_name, last_name, date_of_birth, pan_image_path, status)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, COALESCE($8, 'ACTIVE'))
		 RETURNING id, created_at, updated_at, status`,
		u.Mobile, u.Email, u.PANNumber, u.FirstName, u.LastName,
		u.DateOfBirth, u.PANImagePath, nilString(u.Status),
	)
	if err := row.Scan(&u.ID, &u.CreatedAt, &u.UpdatedAt, &u.Status); err != nil {
		return classifyPgErr(err)
	}
	return nil
}

func (r *UserRepo) exists(ctx context.Context, query string, args ...interface{}) (bool, error) {
	var one int
	err := r.pool.QueryRow(ctx, query, args...).Scan(&one)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return false, err
}

func nilString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
