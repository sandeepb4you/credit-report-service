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

// LoanAssistRepo is the data access layer for Loan Assist applications, the
// slot → document map beside them, and partner profiles. The documents are
// ordinary documents rows, so an account deletion or reset takes their bytes
// with it.
type LoanAssistRepo struct{ pool *pgxpool.Pool }

func NewLoanAssistRepo(pool *pgxpool.Pool) *LoanAssistRepo { return &LoanAssistRepo{pool: pool} }

const loanAssistCols = `id, account_id, status, loan_type, amount_needed, rented, consent_at,
    consent_text, request_code, order_id, paid_at, assigned_partner_id, assigned_at,
    docs_verified_at, expert_contacted_at, guidance_at, closed_at, notes, team_notified_at,
    created_at, updated_at`

// ---- applications -----------------------------------------------------------

// FindDraft returns the account's open application, or ErrNotFound.
func (r *LoanAssistRepo) FindDraft(ctx context.Context, accountID int64) (*models.LoanAssistRequest, error) {
	return r.getOne(ctx,
		`SELECT `+loanAssistCols+` FROM loan_assist_requests
		  WHERE account_id = $1 AND status = 'DRAFT'`, accountID)
}

// EnsureDraft returns the account's open application, creating it for
// loanType if there is none. The partial unique index makes two concurrent
// first calls land on the same row rather than two drafts.
func (r *LoanAssistRepo) EnsureDraft(ctx context.Context, accountID int64, loanType string) (*models.LoanAssistRequest, error) {
	if _, err := r.pool.Exec(ctx,
		`INSERT INTO loan_assist_requests (account_id, loan_type) VALUES ($1, $2)
		 ON CONFLICT (account_id) WHERE status = 'DRAFT' DO NOTHING`, accountID, loanType); err != nil {
		return nil, classifyPgErr(err)
	}
	return r.FindDraft(ctx, accountID)
}

// UpdateDraft changes a draft's loan type, amount and premises. Nil leaves a
// field alone.
func (r *LoanAssistRepo) UpdateDraft(
	ctx context.Context, id int64, loanType *string, amount *int64, rented *bool,
) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE loan_assist_requests
		    SET loan_type     = COALESCE($2, loan_type),
		        amount_needed = COALESCE($3, amount_needed),
		        rented        = COALESCE($4, rented),
		        updated_at    = now()
		  WHERE id = $1 AND status = 'DRAFT'`, id, loanType, amount, rented)
	return classifyPgErr(err)
}

// ListSubmitted returns the account's paid applications, newest first.
func (r *LoanAssistRepo) ListSubmitted(ctx context.Context, accountID int64) ([]models.LoanAssistRequest, error) {
	out := []models.LoanAssistRequest{}
	err := pgxscan.Select(ctx, r.pool, &out,
		`SELECT `+loanAssistCols+` FROM loan_assist_requests
		  WHERE account_id = $1 AND status <> 'DRAFT'
		  ORDER BY paid_at DESC NULLS LAST, id DESC`, accountID)
	return out, err
}

// FindByCode returns one of the account's applications by request code, or
// ErrNotFound — someone else's code reads as missing.
func (r *LoanAssistRepo) FindByCode(ctx context.Context, accountID int64, code string) (*models.LoanAssistRequest, error) {
	return r.getOne(ctx,
		`SELECT `+loanAssistCols+` FROM loan_assist_requests
		  WHERE account_id = $1 AND request_code = $2`, accountID, code)
}

// FindByOrder returns the application an order paid for, or ErrNotFound.
func (r *LoanAssistRepo) FindByOrder(ctx context.Context, orderID int64) (*models.LoanAssistRequest, error) {
	return r.getOne(ctx,
		`SELECT `+loanAssistCols+` FROM loan_assist_requests WHERE order_id = $1`, orderID)
}

// FindByID returns one application, or ErrNotFound.
func (r *LoanAssistRepo) FindByID(ctx context.Context, id int64) (*models.LoanAssistRequest, error) {
	return r.getOne(ctx,
		`SELECT `+loanAssistCols+` FROM loan_assist_requests WHERE id = $1`, id)
}

func (r *LoanAssistRepo) getOne(ctx context.Context, sql string, args ...any) (*models.LoanAssistRequest, error) {
	var req models.LoanAssistRequest
	err := pgxscan.Get(ctx, r.pool, &req, sql, args...)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &req, nil
}

// RecordConsent stamps the consent on a draft, with the sentence agreed to.
// Re-stamped on every submit: the newest agreement is the one payment follows.
func (r *LoanAssistRepo) RecordConsent(ctx context.Context, requestID int64, text string, at time.Time) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE loan_assist_requests
		    SET consent_at = $2, consent_text = $3, updated_at = now()
		  WHERE id = $1 AND status = 'DRAFT'`, requestID, at, text)
	return err
}

// MarkPaid turns the account's draft into a submitted application for order,
// minting its request code. ErrNotFound when the account has no draft.
func (r *LoanAssistRepo) MarkPaid(
	ctx context.Context, accountID, orderID int64, paidAt time.Time,
) (*models.LoanAssistRequest, error) {
	// LA-<id+20000>: short enough to read down a phone line, unique because the
	// id is. Not a secret — every lookup by code is scoped to its account.
	return r.getOne(ctx,
		`UPDATE loan_assist_requests
		    SET status = 'SUBMITTED', order_id = $2, paid_at = $3,
		        request_code = 'LA-' || (id + 20000)::text, updated_at = now()
		  WHERE account_id = $1 AND status = 'DRAFT'
		 RETURNING `+loanAssistCols, accountID, orderID, paidAt)
}

// OrderAmount is what an order charged, or ErrNotFound.
func (r *LoanAssistRepo) OrderAmount(ctx context.Context, orderID int64) (float64, error) {
	var amount float64
	err := r.pool.QueryRow(ctx, `SELECT amount FROM orders WHERE id = $1`, orderID).Scan(&amount)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrNotFound
	}
	return amount, err
}

// MarkTeamNotified records that the team was told, so a replay does not mail twice.
func (r *LoanAssistRepo) MarkTeamNotified(ctx context.Context, requestID int64) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE loan_assist_requests SET team_notified_at = now() WHERE id = $1`, requestID)
	return err
}

// ---- documents --------------------------------------------------------------

const loanDocSelect = `SELECT ld.request_id, ld.slot, ld.document_id,
        d.filename, d.mime_type, d.size_bytes, d.created_at AS uploaded_at,
        ld.review_state, ld.review_note, ld.reviewed_by, ld.reviewed_at, ld.resubmitted_at,
        src.request_code AS reused_from
   FROM loan_assist_documents ld
   JOIN documents d ON d.id = ld.document_id
   LEFT JOIN loan_assist_requests src ON src.id = ld.reused_from_request_id`

// Documents lists one application's filled slots.
func (r *LoanAssistRepo) Documents(ctx context.Context, requestID int64) ([]models.LoanAssistDocument, error) {
	return r.DocumentsOf(ctx, []int64{requestID})
}

// DocumentsOf lists the filled slots of several applications.
func (r *LoanAssistRepo) DocumentsOf(ctx context.Context, requestIDs []int64) ([]models.LoanAssistDocument, error) {
	docs := []models.LoanAssistDocument{}
	if len(requestIDs) == 0 {
		return docs, nil
	}
	err := pgxscan.Select(ctx, r.pool, &docs,
		loanDocSelect+` WHERE ld.request_id = ANY($1) ORDER BY ld.request_id, ld.slot`, requestIDs)
	return docs, err
}

// deleteIfOrphan deletes a document no slot refers to any more, returning its
// object URI ("" when it is still in use elsewhere — a copy reused by another
// application — or already gone).
func deleteIfOrphan(ctx context.Context, tx pgx.Tx, documentID int64) (string, error) {
	var uri string
	err := tx.QueryRow(ctx,
		`DELETE FROM documents d
		  WHERE d.id = $1
		    AND NOT EXISTS (SELECT 1 FROM loan_assist_documents ld WHERE ld.document_id = $1)
		 RETURNING d.s3_uri`, documentID).Scan(&uri)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return uri, err
}

// PutDocument stores doc as a new documents row and makes it the
// application's document for slot, in one transaction, back in review. A
// resubmission (the slot was RETAKE) is stamped "sent again". The file it
// replaces is deleted unless another application still uses it; its object
// URI is returned for the caller to remove after the commit.
func (r *LoanAssistRepo) PutDocument(
	ctx context.Context, requestID int64, slot string, doc *models.Document, resubmission bool,
) (replacedURI string, err error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var oldID *int64
	err = tx.QueryRow(ctx,
		`SELECT document_id FROM loan_assist_documents
		  WHERE request_id = $1 AND slot = $2 FOR UPDATE`, requestID, slot).Scan(&oldID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}

	var newID int64
	if err = tx.QueryRow(ctx,
		`INSERT INTO documents (account_id, doc_type, s3_uri, filename, mime_type, size_bytes)
		 VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`,
		doc.AccountID, doc.DocType, doc.S3URI, doc.FileName, doc.MimeType, doc.SizeBytes,
	).Scan(&newID); err != nil {
		return "", classifyPgErr(err)
	}
	if _, err = tx.Exec(ctx,
		`INSERT INTO loan_assist_documents (request_id, slot, document_id) VALUES ($1, $2, $3)
		 ON CONFLICT (request_id, slot) DO UPDATE
		     SET document_id = EXCLUDED.document_id, review_state = 'PENDING',
		         reviewed_at = NULL, reviewed_by = NULL, reused_from_request_id = NULL,
		         resubmitted_at = CASE WHEN $4 THEN now() ELSE NULL END,
		         review_note = CASE WHEN $4 THEN loan_assist_documents.review_note ELSE NULL END,
		         created_at = now()`,
		requestID, slot, newID, resubmission); err != nil {
		return "", classifyPgErr(err)
	}
	if oldID != nil {
		if replacedURI, err = deleteIfOrphan(ctx, tx, *oldID); err != nil {
			return "", err
		}
	}
	if _, err = tx.Exec(ctx,
		`UPDATE loan_assist_requests SET updated_at = now() WHERE id = $1`, requestID); err != nil {
		return "", err
	}
	if err = tx.Commit(ctx); err != nil {
		return "", err
	}
	return replacedURI, nil
}

// LinkDocument fills a draft's slot with a document already accepted in
// another of the account's applications — linked, not copied — and carries
// the acceptance with it. Returns the URI of a file it displaced, if orphaned.
func (r *LoanAssistRepo) LinkDocument(
	ctx context.Context, requestID int64, slot string, saved models.SavedLoanDocument,
) (string, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var oldID *int64
	err = tx.QueryRow(ctx,
		`SELECT document_id FROM loan_assist_documents
		  WHERE request_id = $1 AND slot = $2 FOR UPDATE`, requestID, slot).Scan(&oldID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	if _, err = tx.Exec(ctx,
		`INSERT INTO loan_assist_documents
		     (request_id, slot, document_id, review_state, reviewed_at, reused_from_request_id)
		 VALUES ($1, $2, $3, 'ACCEPTED', $4, $5)
		 ON CONFLICT (request_id, slot) DO UPDATE
		     SET document_id = EXCLUDED.document_id, review_state = 'ACCEPTED',
		         review_note = NULL, reviewed_by = NULL, reviewed_at = EXCLUDED.reviewed_at,
		         resubmitted_at = NULL, reused_from_request_id = EXCLUDED.reused_from_request_id,
		         created_at = now()`,
		requestID, slot, saved.DocumentID, saved.VerifiedAt, saved.RequestID); err != nil {
		return "", classifyPgErr(err)
	}
	var uri string
	if oldID != nil && *oldID != saved.DocumentID {
		if uri, err = deleteIfOrphan(ctx, tx, *oldID); err != nil {
			return "", err
		}
	}
	return uri, tx.Commit(ctx)
}

// RemoveSlots empties the given slots of an application, deleting each file
// no other application uses. Returns the object URIs to remove.
func (r *LoanAssistRepo) RemoveSlots(ctx context.Context, requestID int64, slots []string) ([]string, error) {
	if len(slots) == 0 {
		return nil, nil
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	rows, err := tx.Query(ctx,
		`DELETE FROM loan_assist_documents WHERE request_id = $1 AND slot = ANY($2)
		 RETURNING document_id`, requestID, slots)
	if err != nil {
		return nil, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var uris []string
	for _, id := range ids {
		uri, err := deleteIfOrphan(ctx, tx, id)
		if err != nil {
			return nil, err
		}
		if uri != "" {
			uris = append(uris, uri)
		}
	}
	return uris, tx.Commit(ctx)
}

// SavedDocuments is, per slot, the newest document a partner accepted in one
// of the account's paid applications — what a new application may reuse.
func (r *LoanAssistRepo) SavedDocuments(ctx context.Context, accountID int64) ([]models.SavedLoanDocument, error) {
	out := []models.SavedLoanDocument{}
	err := pgxscan.Select(ctx, r.pool, &out,
		`SELECT DISTINCT ON (ld.slot)
		        ld.slot, ld.document_id, d.filename, d.created_at AS uploaded_at,
		        ld.reviewed_at, la.id AS request_id, la.request_code
		   FROM loan_assist_documents ld
		   JOIN loan_assist_requests la ON la.id = ld.request_id
		   JOIN documents d ON d.id = ld.document_id
		  WHERE la.account_id = $1 AND la.status <> 'DRAFT'
		    AND ld.review_state = 'ACCEPTED' AND ld.reviewed_at IS NOT NULL
		  ORDER BY ld.slot, ld.reviewed_at DESC`, accountID)
	return out, err
}

// ---- partner review ---------------------------------------------------------

// ReviewScope narrows the partners' list.
const (
	ReviewScopeMine       = "mine"       // assigned to the caller
	ReviewScopeUnassigned = "unassigned" // nobody yet
	ReviewScopeAll        = "all"        // admins only
)

// ReviewFilter narrows the partners' list. PartnerID is the caller; for a
// partner (not admin) the list never reaches beyond "mine" and "unassigned".
type ReviewFilter struct {
	Scope     string
	PartnerID int64
	Status    string
	Limit     int
	Offset    int
}

const reviewSelect = `SELECT la.id, la.request_code, la.status, la.loan_type, la.amount_needed,
        la.rented, la.paid_at, la.consent_at, la.account_id,
        TRIM(COALESCE(a.first_name, '') || ' ' || COALESCE(a.last_name, '')) AS customer_name,
        a.primary_phone, a.primary_email, o.amount, o.payment_mode,
        la.assigned_partner_id,
        COALESCE(pp.display_name,
                 NULLIF(TRIM(COALESCE(pa.first_name, '') || ' ' || COALESCE(pa.last_name, '')), '')) AS assigned_name,
        la.notes, la.updated_at,
        (SELECT count(*) FROM loan_assist_documents x WHERE x.request_id = la.id) AS doc_count,
        (SELECT count(*) FROM loan_assist_documents x WHERE x.request_id = la.id AND x.review_state = 'PENDING') AS pending_count,
        (SELECT count(*) FROM loan_assist_documents x WHERE x.request_id = la.id AND x.review_state = 'RETAKE') AS retake_count
   FROM loan_assist_requests la
   JOIN accounts a ON a.id = la.account_id
   LEFT JOIN orders o ON o.id = la.order_id
   LEFT JOIN accounts pa ON pa.id = la.assigned_partner_id
   LEFT JOIN assist_partner_profiles pp ON pp.account_id = la.assigned_partner_id`

// ListForReview returns paid applications for the partners' list, the ones
// with documents waiting to be checked first, and the total.
func (r *LoanAssistRepo) ListForReview(ctx context.Context, f ReviewFilter) ([]models.ReviewApplication, int, error) {
	where := ` WHERE la.status <> 'DRAFT'
	   AND ($1::text IS NULL OR la.status = $1)
	   AND CASE $2::text
	         WHEN 'mine'       THEN la.assigned_partner_id = $3
	         WHEN 'unassigned' THEN la.assigned_partner_id IS NULL
	         ELSE TRUE END`
	var status *string
	if f.Status != "" {
		status = &f.Status
	}
	var total int
	if err := r.pool.QueryRow(ctx,
		`SELECT count(*) FROM loan_assist_requests la`+where, status, f.Scope, f.PartnerID).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows := []models.ReviewApplication{}
	err := pgxscan.Select(ctx, r.pool, &rows,
		reviewSelect+where+`
		  ORDER BY (la.status = 'SUBMITTED' AND EXISTS (
		              SELECT 1 FROM loan_assist_documents x
		               WHERE x.request_id = la.id AND x.review_state = 'PENDING')) DESC,
		           la.paid_at DESC NULLS LAST, la.id DESC
		  LIMIT $4 OFFSET $5`, status, f.Scope, f.PartnerID, f.Limit, f.Offset)
	return rows, total, err
}

// FindForReview returns one paid application as the partners' list draws
// it, or ErrNotFound (drafts are the customer's and never listed).
func (r *LoanAssistRepo) FindForReview(ctx context.Context, id int64) (*models.ReviewApplication, error) {
	var row models.ReviewApplication
	err := pgxscan.Get(ctx, r.pool, &row, reviewSelect+` WHERE la.id = $1 AND la.status <> 'DRAFT'`, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// ReviewDocument records a partner's verdict on one slot. ErrNotFound when
// the slot is empty.
func (r *LoanAssistRepo) ReviewDocument(
	ctx context.Context, requestID int64, slot, state string, note *string, reviewerID int64,
) error {
	tag, err := r.pool.Exec(ctx,
		`UPDATE loan_assist_documents
		    SET review_state = $3, review_note = $4, reviewed_by = $5, reviewed_at = now()
		  WHERE request_id = $1 AND slot = $2`, requestID, slot, state, note, reviewerID)
	if err != nil {
		return classifyPgErr(err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	_, err = r.pool.Exec(ctx, `UPDATE loan_assist_requests SET updated_at = now() WHERE id = $1`, requestID)
	return err
}

// SetStage moves a paid application to a status, stamping when each stage
// was first reached so the customer's progress steps can date them.
func (r *LoanAssistRepo) SetStage(ctx context.Context, id int64, status string) error {
	tag, err := r.pool.Exec(ctx,
		`UPDATE loan_assist_requests
		    SET status = $2::text,
		        docs_verified_at    = CASE WHEN $2::text IN ('DOCS_VERIFIED','EXPERT_CONTACTED','GUIDANCE','CLOSED')
		                                   THEN COALESCE(docs_verified_at, now()) ELSE NULL END,
		        expert_contacted_at = CASE WHEN $2::text IN ('EXPERT_CONTACTED','GUIDANCE','CLOSED')
		                                   THEN COALESCE(expert_contacted_at, now()) ELSE NULL END,
		        guidance_at         = CASE WHEN $2::text IN ('GUIDANCE','CLOSED')
		                                   THEN COALESCE(guidance_at, now()) ELSE NULL END,
		        closed_at           = CASE WHEN $2::text = 'CLOSED' THEN COALESCE(closed_at, now()) ELSE NULL END,
		        updated_at = now()
		  WHERE id = $1 AND status <> 'DRAFT'`, id, status)
	if err != nil {
		return classifyPgErr(err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// Assign gives a paid application to a partner (nil = unassign).
func (r *LoanAssistRepo) Assign(ctx context.Context, id int64, partnerID *int64) error {
	tag, err := r.pool.Exec(ctx,
		`UPDATE loan_assist_requests
		    SET assigned_partner_id = $2,
		        assigned_at = CASE WHEN $2::bigint IS NULL THEN NULL ELSE now() END,
		        updated_at = now()
		  WHERE id = $1 AND status <> 'DRAFT'`, id, partnerID)
	if err != nil {
		return classifyPgErr(err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// SetNotes replaces the partners' notes ("" clears them).
func (r *LoanAssistRepo) SetNotes(ctx context.Context, id int64, notes string) error {
	tag, err := r.pool.Exec(ctx,
		`UPDATE loan_assist_requests SET notes = NULLIF($2, ''), updated_at = now()
		  WHERE id = $1 AND status <> 'DRAFT'`, id, notes)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// TeamDocument returns the stored document filling a slot of a paid
// application, or ErrNotFound.
func (r *LoanAssistRepo) TeamDocument(ctx context.Context, requestID int64, slot string) (*models.Document, error) {
	var d models.Document
	err := pgxscan.Get(ctx, r.pool, &d,
		`SELECT d.id, d.account_id, d.doc_type, d.s3_uri, d.filename, d.mime_type,
		        d.size_bytes, d.created_at, d.updated_at
		   FROM loan_assist_documents ld
		   JOIN documents d ON d.id = ld.document_id
		   JOIN loan_assist_requests la ON la.id = ld.request_id
		  WHERE ld.request_id = $1 AND ld.slot = $2 AND la.status <> 'DRAFT'`, requestID, slot)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &d, nil
}

// ---- partners ---------------------------------------------------------------

// Partners lists every account an application can be assigned to: partners
// and admins, with their profile where they have one.
func (r *LoanAssistRepo) Partners(ctx context.Context) ([]models.PartnerOption, error) {
	out := []models.PartnerOption{}
	err := pgxscan.Select(ctx, r.pool, &out,
		`SELECT a.id,
		        COALESCE(pp.display_name,
		                 NULLIF(TRIM(COALESCE(a.first_name, '') || ' ' || COALESCE(a.last_name, '')), ''),
		                 a.primary_email, a.primary_phone, 'Account ' || a.id) AS name,
		        pp.team, pp.city, a.role
		   FROM accounts a
		   LEFT JOIN assist_partner_profiles pp ON pp.account_id = a.id
		  WHERE a.role IN ('partner', 'agent', 'admin') AND a.status = 'ACTIVE'
		  ORDER BY a.role DESC, name`)
	return out, err
}

// PartnerCard returns how a partner appears to a customer: their profile, or
// failing that their account name under the default team.
func (r *LoanAssistRepo) PartnerCard(ctx context.Context, accountID int64) (*models.PartnerCard, error) {
	var c models.PartnerCard
	err := pgxscan.Get(ctx, r.pool, &c,
		`SELECT a.id AS account_id,
		        COALESCE(pp.display_name,
		                 NULLIF(TRIM(COALESCE(a.first_name, '') || ' ' || COALESCE(a.last_name, '')), ''),
		                 'Your myScorr expert') AS display_name,
		        COALESCE(pp.team, 'Online Support') AS team, pp.city, pp.call_phone
		   FROM accounts a
		   LEFT JOIN assist_partner_profiles pp ON pp.account_id = a.id
		  WHERE a.id = $1`, accountID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// UpsertProfile saves a partner's own card.
func (r *LoanAssistRepo) UpsertProfile(ctx context.Context, c models.PartnerCard) error {
	_, err := r.pool.Exec(ctx,
		`INSERT INTO assist_partner_profiles (account_id, display_name, team, city, call_phone)
		 VALUES ($1, $2, $3, $4, $5)
		 ON CONFLICT (account_id) DO UPDATE
		     SET display_name = EXCLUDED.display_name, team = EXCLUDED.team,
		         city = EXCLUDED.city, call_phone = EXCLUDED.call_phone, updated_at = now()`,
		c.AccountID, c.Name, c.Team, c.City, c.CallPhone)
	return classifyPgErr(err)
}
