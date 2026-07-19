// Package repository provides Postgres persistence via pgx + scany.
// Each repository is a thin struct over *pgxpool.Pool; transactions are passed
// as pgx.Tx so service code can group operations atomically.
package repository

import (
	"context"
	"errors"

	"github.com/georgysavva/scany/v2/pgxscan"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"credit-report-service/internal/models"
)

// CreditReportRepo owns credit_reports queries.
type CreditReportRepo struct{ pool *pgxpool.Pool }

func NewCreditReportRepo(pool *pgxpool.Pool) *CreditReportRepo {
	return &CreditReportRepo{pool: pool}
}

// Getter abstracts *pgxpool.Pool vs pgx.Tx so queries can run in either.
type Getter interface {
	Query(ctx context.Context, sql string, args ...interface{}) (pgx.Rows, error)
}

func (r *CreditReportRepo) Pool() *pgxpool.Pool { return r.pool }

func (r *CreditReportRepo) FindAll(ctx context.Context) ([]models.CreditReport, error) {
	var rs []models.CreditReport
	err := pgxscan.Select(ctx, r.pool, &rs,
		`SELECT id, subject_id, score, status, created_at, updated_at
		 FROM credit_reports
		 ORDER BY id`)
	return rs, err
}

func (r *CreditReportRepo) FindByID(ctx context.Context, id int64) (*models.CreditReport, error) {
	var cr models.CreditReport
	err := pgxscan.Get(ctx, r.pool, &cr,
		`SELECT id, subject_id, score, status, created_at, updated_at
		 FROM credit_reports
		 WHERE id = $1`, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &cr, err
}

func (r *CreditReportRepo) FindBySubjectID(ctx context.Context, subjectID string) (*models.CreditReport, error) {
	var cr models.CreditReport
	err := pgxscan.Get(ctx, r.pool, &cr,
		`SELECT id, subject_id, score, status, created_at, updated_at
		 FROM credit_reports
		 WHERE subject_id = $1`, subjectID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &cr, err
}

func (r *CreditReportRepo) Create(ctx context.Context, cr *models.CreditReport) error {
	return pgxscan.Get(ctx, r.pool, cr,
		`INSERT INTO credit_reports (subject_id, score, status)
		 VALUES ($1, $2, $3)
		 RETURNING id, created_at, updated_at`,
		cr.SubjectID, cr.Score, cr.Status,
	)
}

func (r *CreditReportRepo) DeleteByID(ctx context.Context, id int64) error {
	cmd, err := r.pool.Exec(ctx, `DELETE FROM credit_reports WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if cmd.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
