package repository

import (
	"context"
	"errors"
	"time"

	"github.com/georgysavva/scany/v2/pgxscan"
	"github.com/jackc/pgx/v5"

	"credit-report-service/internal/models"
)

const documentCols = `id, account_id, doc_type, s3_uri, filename, mime_type,
    size_bytes, created_at, updated_at`

// FindDocumentByID returns one stored document row, or ErrNotFound.
func (r *AccountRepo) FindDocumentByID(ctx context.Context, id int64) (*models.Document, error) {
	var d models.Document
	err := pgxscan.Get(ctx, r.pool, &d,
		`SELECT `+documentCols+` FROM documents WHERE id = $1`, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &d, nil
}

// AttachKYCDocument stores doc as a new documents row and points the KYC
// record at it, in one transaction: the record must never cite a document
// that was not stored, nor the reverse. Earlier uploads stay behind as
// history — the pointer, not the table, says which card is current.
//
// A REJECTED record returns to PENDING — the upload is the user's answer to
// the rejection — and the stale reason and reviewer stamp go with it. dob,
// when non-nil, lands beside the PAN; nil keeps what is already there.
func (r *AccountRepo) AttachKYCDocument(
	ctx context.Context, doc *models.Document, dob *time.Time,
) (*models.KYCRecord, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var stored models.Document
	err = pgxscan.Get(ctx, tx, &stored,
		`INSERT INTO documents (account_id, doc_type, s3_uri, filename, mime_type, size_bytes)
		 VALUES ($1, $2, $3, $4, $5, $6)
		 RETURNING `+documentCols,
		doc.AccountID, doc.DocType, doc.S3URI, doc.FileName, doc.MimeType, doc.SizeBytes,
	)
	if err != nil {
		return nil, classifyPgErr(err)
	}

	var k models.KYCRecord
	err = pgxscan.Get(ctx, tx, &k,
		`UPDATE kyc_records
		    SET document_id       = $2,
		        pan_date_of_birth = COALESCE($3, kyc_records.pan_date_of_birth),
		        status            = CASE WHEN status = 'REJECTED' THEN 'PENDING' ELSE status END,
		        rejection_reason  = CASE WHEN status = 'REJECTED' THEN NULL ELSE rejection_reason END,
		        reviewed_by_account_id = CASE WHEN status = 'REJECTED' THEN NULL ELSE reviewed_by_account_id END,
		        reviewed_at       = CASE WHEN status = 'REJECTED' THEN NULL ELSE reviewed_at END,
		        updated_at        = now()
		  WHERE account_id = $1
		 RETURNING `+kycCols,
		doc.AccountID, stored.ID, dob,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, classifyPgErr(err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &k, nil
}
