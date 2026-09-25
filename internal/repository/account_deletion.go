package repository

import (
	"context"
	"errors"
	"time"

	"github.com/georgysavva/scany/v2/pgxscan"
	"github.com/jackc/pgx/v5"

	"credit-report-service/internal/models"
)

// Package repository — user-initiated account deletion.
//
// Two halves that must not be confused with internal/repository/account_reset.go:
// a RESET walks an account back to signup and deliberately KEEPS the login, so
// the same number can be tested through onboarding again. A PURGE destroys the
// person and keeps only the money. They share a lot of DELETE statements and
// nothing else.
//
// What survives a purge, and why:
//
//	orders, payment_webhook_events,  the financial record. Indian bookkeeping
//	invoices                         and tax rules require it to outlive the
//	                                 customer, and a payment later disputed
//	                                 with Cashfree has to be answerable.
//	referral_earnings                money. An earning credited to SOMEBODY
//	referral_withdrawals             ELSE off this user's first purchase is
//	                                 their money, not this user's, and must
//	                                 not vanish because this user left.
//	accounts (anonymised)            only because the three above have a
//	                                 NOT NULL foreign key into it. Every
//	                                 personal column is nulled, which is what
//	                                 makes the retained rows point at nobody.
//
// Pointing at nobody is half of it; the rows must also SAY nothing. The
// webhook payload is rebuilt from an allowlist (Cashfree echoes the customer's
// name, phone and email back in it), an invoice loses its billed-to columns and
// its rendered PDF, and a withdrawal's holder name is replaced. Those scrubs
// are what make the privacy policy's "kept with your name, number and email
// removed" true — change what is retained and that sentence, on
// myscorr.com/privacy-policy#retention and /delete-account and in the app's
// DeleteAccountScreen, changes with it.
//
// Everything else goes, including every auth_identities row — that is what
// makes the account unreachable. A phone account keeps its login in
// accounts.primary_phone rather than an identity row, so nulling that column
// is the other half of the same job; miss it and the number signs straight
// back in to a hollowed-out account.

const accountDeletionRequestCols = `id, account_id, status, channel, destination_masked,
	requested_at, scheduled_for, cancelled_at, cancelled_reason, completed_at`

// ---- the grant -----------------------------------------------------------

// CreateDeletionToken stores the digest of a freshly minted deletion grant.
func (r *AccountRepo) CreateDeletionToken(
	ctx context.Context, tx pgx.Tx, accountID int64, tokenHash string, expiresAt time.Time,
) error {
	_, err := tx.Exec(ctx,
		`INSERT INTO account_deletion_tokens (account_id, token_hash, expires_at)
		 VALUES ($1, $2, $3)`,
		accountID, tokenHash, expiresAt)
	return classifyPgErr(err)
}

// InvalidateDeletionTokens burns every live grant on the account, so re-running
// the flow cannot leave an older token redeemable.
func (r *AccountRepo) InvalidateDeletionTokens(
	ctx context.Context, tx pgx.Tx, accountID int64,
) error {
	_, err := tx.Exec(ctx,
		`UPDATE account_deletion_tokens SET consumed_at = now()
		  WHERE account_id = $1 AND consumed_at IS NULL`, accountID)
	return classifyPgErr(err)
}

// ConsumeDeletionToken redeems a grant and returns the account it belongs to.
//
// A compare-and-set on consumed_at rather than a read followed by a write: two
// confirms racing on the same token must not both schedule a deletion, and the
// unique index on pending requests would turn the second into a 500 rather
// than the no-op it should be.
//
// Expiry is part of the same predicate, so "wrong", "expired" and "already
// used" are one ErrNotFound. The caller must not tell them apart for the
// client — the next step is identical in all three cases.
func (r *AccountRepo) ConsumeDeletionToken(
	ctx context.Context, tx pgx.Tx, tokenHash string,
) (int64, error) {
	var accountID int64
	err := tx.QueryRow(ctx,
		`UPDATE account_deletion_tokens SET consumed_at = now()
		  WHERE token_hash = $1 AND consumed_at IS NULL AND expires_at > now()
		 RETURNING account_id`, tokenHash).Scan(&accountID)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrNotFound
	}
	return accountID, classifyPgErr(err)
}

// ---- the request ---------------------------------------------------------

// CreateDeletionRequest schedules the purge, reporting whether it created the
// request or found one already live.
//
// ON CONFLICT DO NOTHING against the one-pending-per-account index, then a
// read: a second confirm while a request is already live returns the EXISTING
// date rather than moving the deadline. Someone who runs the flow twice should
// not be able to walk their own grace period forward, and — more to the point —
// neither should someone who has stolen one OTP.
//
// The `created` flag comes from whether the INSERT's RETURNING produced a row,
// NOT from comparing the stored timestamp against the one passed in. That
// comparison is the obvious implementation and it is wrong: Postgres stores
// timestamptz at microsecond precision while a Go time.Time carries
// nanoseconds, so a freshly inserted row never compares equal to the value
// that created it — and every first-time caller was told their account had
// "already" been scheduled.
func (r *AccountRepo) CreateDeletionRequest(
	ctx context.Context, tx pgx.Tx,
	accountID int64, channel, destinationMasked string, scheduledFor time.Time,
) (req *models.AccountDeletionRequest, created bool, err error) {
	var inserted models.AccountDeletionRequest
	err = pgxscan.Get(ctx, tx, &inserted,
		`INSERT INTO account_deletion_requests
		     (account_id, status, channel, destination_masked, scheduled_for)
		 VALUES ($1, $2, $3, $4, $5)
		 ON CONFLICT DO NOTHING
		 RETURNING `+accountDeletionRequestCols,
		accountID, models.DeletionPending, channel, destinationMasked, scheduledFor)
	if err == nil {
		return &inserted, true, nil
	}
	if !pgxscan.NotFound(err) {
		return nil, false, classifyPgErr(err)
	}

	// No row returned: the unique index rejected the insert, so one is already
	// pending. Read it and report its date.
	var existing models.AccountDeletionRequest
	err = pgxscan.Get(ctx, tx, &existing,
		`SELECT `+accountDeletionRequestCols+` FROM account_deletion_requests
		  WHERE account_id = $1 AND status = $2`,
		accountID, models.DeletionPending)
	if pgxscan.NotFound(err) {
		return nil, false, ErrNotFound
	}
	return &existing, false, classifyPgErr(err)
}

// FindPendingDeletion returns the account's live request, or ErrNotFound.
func (r *AccountRepo) FindPendingDeletion(
	ctx context.Context, accountID int64,
) (*models.AccountDeletionRequest, error) {
	var req models.AccountDeletionRequest
	err := pgxscan.Get(ctx, r.pool, &req,
		`SELECT `+accountDeletionRequestCols+` FROM account_deletion_requests
		  WHERE account_id = $1 AND status = $2`,
		accountID, models.DeletionPending)
	if pgxscan.NotFound(err) {
		return nil, ErrNotFound
	}
	return &req, classifyPgErr(err)
}

// CancelPendingDeletion stops a live request, returning whether one was found.
//
// The boolean matters: this runs on every successful sign-in, where the
// overwhelmingly common answer is "there was nothing to cancel". Only a true
// return should produce a log line or tell the user anything.
func (r *AccountRepo) CancelPendingDeletion(
	ctx context.Context, accountID int64, reason string,
) (bool, error) {
	tag, err := r.pool.Exec(ctx,
		`UPDATE account_deletion_requests
		    SET status = $3, cancelled_at = now(), cancelled_reason = $4
		  WHERE account_id = $1 AND status = $2`,
		accountID, models.DeletionPending, models.DeletionCancelled, reason)
	if err != nil {
		return false, classifyPgErr(err)
	}
	return tag.RowsAffected() > 0, nil
}

// ListDueDeletions returns the requests whose grace period has run out.
//
// A plain read rather than a claim-with-lock: unlike the scheduled-check
// runner this sweep is single-writer (one deployment, one loop) and the purge
// is idempotent through the status transition — CompleteDeletionRequest only
// moves a row out of PENDING, so a repeat pass finds nothing to do.
func (r *AccountRepo) ListDueDeletions(
	ctx context.Context, now time.Time, limit int,
) ([]models.AccountDeletionRequest, error) {
	var out []models.AccountDeletionRequest
	err := pgxscan.Select(ctx, r.pool, &out,
		`SELECT `+accountDeletionRequestCols+` FROM account_deletion_requests
		  WHERE status = $1 AND scheduled_for <= $2
		  ORDER BY scheduled_for
		  LIMIT $3`,
		models.DeletionPending, now, limit)
	return out, classifyPgErr(err)
}

// CompleteDeletionRequest marks a request carried out. Guarded on PENDING so a
// second pass over the same row cannot rewrite completed_at.
func (r *AccountRepo) CompleteDeletionRequest(ctx context.Context, requestID int64) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE account_deletion_requests
		    SET status = $2, completed_at = now()
		  WHERE id = $1 AND status = $3`,
		requestID, models.DeletionCompleted, models.DeletionPending)
	return classifyPgErr(err)
}

// ---- the purge -----------------------------------------------------------

// PurgeAccount destroys everything personal about an account in one
// transaction, leaving an anonymous shell behind for the financial rows to
// hang off.
//
// One transaction because a half-purge is the worst outcome available: an
// account whose identities are gone but whose PAN and bureau report remain is
// both unreachable by its owner and still full of their data, with nothing
// left that can ask for it to be removed.
//
// ORDER MATTERS, and the reasons are the same ones account_reset.go documents
// the hard way: coupon_redemptions references order_uid, scheduled_score_checks
// holds an order_id FK with NO ACTION. Both are deleted here while orders are
// RETAINED, so neither ordering hazard applies — but any new table pointing at
// accounts or orders needs a line here as well as there, and forgetting it
// fails this transaction rather than silently skipping data.
func (r *AccountRepo) PurgeAccount(
	ctx context.Context, accountID int64,
) (*models.AccountPurgeResult, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	res := &models.AccountPurgeResult{AccountID: accountID}

	// The account's own contact details, read before they are nulled: the
	// signup_tokens table is keyed on the EMAIL rather than an account id (no
	// account exists when one is written), so it can only be cleaned up by
	// address, and after the UPDATE below there is no address left to use.
	var email, phone *string
	err = tx.QueryRow(ctx,
		`SELECT primary_email, primary_phone FROM accounts WHERE id = $1`,
		accountID).Scan(&email, &phone)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}

	// Reports first, keeping the bureau PDF URIs and the ids: the rows are about
	// to go and their objects have to follow them out. The ids are for the
	// advanced report, whose key is derived rather than stored — see
	// AccountPurgeResult.DeletedReportIDs.
	reportRows, err := tx.Query(ctx,
		`DELETE FROM credit_analytics_requests WHERE account_id = $1
		 RETURNING id, result_pdf_url`, accountID)
	if err != nil {
		return nil, err
	}
	for reportRows.Next() {
		var id int64
		var uri *string
		if err := reportRows.Scan(&id, &uri); err != nil {
			reportRows.Close()
			return nil, err
		}
		res.Reports++
		res.DeletedReportIDs = append(res.DeletedReportIDs, id)
		if uri != nil && *uri != "" {
			res.ObjectURIs = append(res.ObjectURIs, *uri)
		}
	}
	reportRows.Close()
	if err := reportRows.Err(); err != nil {
		return nil, err
	}
	// Uploaded documents — the PAN card image and whatever else onboarding
	// collects — ride the same bucket cleanup.
	if res.ObjectURIs, res.Documents, err = deleteReturningURIs(ctx, tx,
		`DELETE FROM documents WHERE account_id = $1
		 RETURNING s3_uri`, accountID, res.ObjectURIs); err != nil {
		return nil, err
	}

	err = tx.QueryRow(ctx,
		`SELECT (SELECT count(*) FROM kyc_records WHERE account_id = $1) > 0,
		        (SELECT count(*) FROM orders      WHERE account_id = $1)`,
		accountID).Scan(&res.HadKYCRecord, &res.RetainedOrders)
	if err != nil {
		return nil, err
	}

	// Counted deletes, in dependency order. Each returns a row count so the
	// audit line can say what actually went.
	for _, step := range []struct {
		into *int
		stmt string
	}{
		{&res.BankStatements, `DELETE FROM bank_statements WHERE account_id = $1`},
		{&res.ScheduledChecks, `DELETE FROM scheduled_score_checks WHERE account_id = $1`},
		{&res.PayoutBankDetails, `DELETE FROM payout_bank_accounts WHERE account_id = $1`},
		{&res.Sessions, `DELETE FROM sessions WHERE account_id = $1`},
		// Last of the counted set: dropping every identity is what makes the
		// account unreachable, and it must not happen before the rows above
		// have gone in the same transaction.
		{&res.Identities, `DELETE FROM auth_identities WHERE account_id = $1`},
	} {
		tag, err := tx.Exec(ctx, step.stmt, accountID)
		if err != nil {
			return nil, err
		}
		*step.into = int(tag.RowsAffected())
	}

	// Uncounted deletes: personal data nobody needs a figure for.
	//
	// sessions above are DELETED, not revoked as a reset does. A revoked
	// session row keeps the IP address and user-agent it was created from,
	// which is personal data under DPDP; the reset keeps them because that
	// account still exists and the rows are its forensic history, and here
	// there is no account left for them to be the history of.
	for _, stmt := range []string{
		`DELETE FROM coupon_redemptions      WHERE account_id = $1`,
		`DELETE FROM kyc_records             WHERE account_id = $1`,
		`DELETE FROM prefill_lookups         WHERE account_id = $1`,
		`DELETE FROM password_reset_tokens   WHERE account_id = $1`,
		`DELETE FROM account_deletion_tokens WHERE account_id = $1`,
	} {
		if _, err := tx.Exec(ctx, stmt, accountID); err != nil {
			return nil, err
		}
	}

	// OTP challenges by account AND by destination. A challenge is keyed on the
	// phone number or address it was sent to, and the one that created a phone
	// account was raised before the account existed — its account_id is NULL.
	// Deleting by account id alone leaves that row, and the number on it.
	contacts := make([]string, 0, 2)
	for _, c := range []*string{email, phone} {
		if c != nil && *c != "" {
			contacts = append(contacts, *c)
		}
	}
	if _, err := tx.Exec(ctx,
		`DELETE FROM otp_challenges WHERE account_id = $1 OR destination = ANY($2)`,
		accountID, contacts); err != nil {
		return nil, err
	}

	// ---- the retained rows, made anonymous in CONTENT as well as in link ----
	//
	// Keeping a row "with the link to you severed" only means something if the
	// row itself says nothing about you. Two of the retained tables did.

	// Cashfree's webhook payload is stored raw, and it echoes back the
	// customer_details the backend sent when creating the order — name, phone,
	// email — plus the payment instrument (a UPI id is a person's handle).
	// Rebuilt from an ALLOWLIST rather than by deleting known keys: a field
	// Cashfree adds next year should be dropped by default, not kept by default.
	// What survives is what reconciling a payment with Cashfree needs: the
	// event, the order, the amount, and Cashfree's own payment references.
	tag, err := tx.Exec(ctx,
		`UPDATE payment_webhook_events SET payload = jsonb_strip_nulls(jsonb_build_object(
		     'type',       payload->'type',
		     'event_time', payload->'event_time',
		     'data', jsonb_build_object(
		         'order', jsonb_build_object(
		             'order_id',       payload->'data'->'order'->'order_id',
		             'order_amount',   payload->'data'->'order'->'order_amount',
		             'order_currency', payload->'data'->'order'->'order_currency'),
		         'payment', jsonb_build_object(
		             'cf_payment_id',    payload->'data'->'payment'->'cf_payment_id',
		             'payment_status',   payload->'data'->'payment'->'payment_status',
		             'payment_amount',   payload->'data'->'payment'->'payment_amount',
		             'payment_currency', payload->'data'->'payment'->'payment_currency',
		             'payment_time',     payload->'data'->'payment'->'payment_time',
		             'payment_group',    payload->'data'->'payment'->'payment_group',
		             'bank_reference',   payload->'data'->'payment'->'bank_reference'))))
		  WHERE order_uid IN (SELECT order_uid FROM orders WHERE account_id = $1)`,
		accountID)
	if err != nil {
		return nil, err
	}
	res.WebhookEventsScrubbed = int(tag.RowsAffected())

	// Invoices are kept for the same reason as the orders they bill (they ARE
	// the tax record, and their numbers are a series that must not develop
	// holes) and scrubbed for the same reason as the webhook log: the billed-to
	// columns name the person. Nothing rule 46 needs for a B2C supply under
	// Rs 50,000 is in them. The rendered PDF prints those same details, so it
	// is deleted with the other objects, and pdf_uri cleared: re-rendering the
	// scrubbed row is what an auditor's copy looks like from now on. A mail
	// still waiting to go out is cancelled; there is nobody to send it to.
	pdfRows, err := tx.Query(ctx,
		`SELECT pdf_uri FROM invoices WHERE account_id = $1 AND pdf_uri IS NOT NULL`, accountID)
	if err != nil {
		return nil, err
	}
	for pdfRows.Next() {
		var uri string
		if err := pdfRows.Scan(&uri); err != nil {
			pdfRows.Close()
			return nil, err
		}
		res.ObjectURIs = append(res.ObjectURIs, uri)
	}
	pdfRows.Close()
	if err := pdfRows.Err(); err != nil {
		return nil, err
	}
	tag, err = tx.Exec(ctx,
		`UPDATE invoices SET billed_to_name = NULL, billed_to_email = NULL, billed_to_phone = NULL,
		        pdf_uri = NULL, next_attempt_at = NULL,
		        auto_email = CASE WHEN auto_email = 'PENDING' THEN 'CANCELLED' ELSE auto_email END,
		        updated_at = now()
		  WHERE account_id = $1`, accountID)
	if err != nil {
		return nil, err
	}
	res.InvoicesScrubbed = int(tag.RowsAffected())
	// The send log holds only digests of addresses, but a digest of a known
	// address is still that address to anyone holding it. It exists to count,
	// and there is nothing left to count for.
	if _, err := tx.Exec(ctx,
		`DELETE FROM invoice_email_sends WHERE account_id = $1`, accountID); err != nil {
		return nil, err
	}

	// A withdrawal not yet paid cannot be paid any more — the payout bank
	// account it named was deleted above. Rejected with a reason, the same
	// state an admin's rejection leaves, so the queue does not hold a request
	// nobody can clear. This is what the deletion page's "referral earnings
	// not yet paid out" line refers to.
	tag, err = tx.Exec(ctx,
		`UPDATE referral_withdrawals
		    SET status = 'REJECTED', decided_at = now(),
		        reject_reason = 'Account deleted before payout'
		  WHERE account_id = $1 AND status = 'PENDING'`, accountID)
	if err != nil {
		return nil, err
	}
	res.PendingWithdrawalsCancelled = int(tag.RowsAffected())

	// Withdrawals are kept (money paid out is a transaction record), but the
	// account holder's name goes. The last four digits and the IFSC stay: they
	// identify the bank account the money went to, which is what matching the
	// payout against the company's own bank statement needs, and neither names
	// a person on its own. The privacy policy says so in those terms.
	tag, err = tx.Exec(ctx,
		`UPDATE referral_withdrawals SET holder_name = '[deleted]'
		  WHERE account_id = $1 AND holder_name <> '[deleted]'`, accountID)
	if err != nil {
		return nil, err
	}
	res.WithdrawalsRedacted = int(tag.RowsAffected())

	// The account's own referral code stops working. Left live, a new signup
	// typing it would be attributed to a tombstone and, on their first
	// purchase, credit money to an account nobody can sign in to.
	tag, err = tx.Exec(ctx,
		`UPDATE coupons SET revoked_at = now(), updated_at = now()
		  WHERE created_by = $1 AND kind = 'referral' AND revoked_at IS NULL`, accountID)
	if err != nil {
		return nil, err
	}
	res.ReferralCodesRevoked = int(tag.RowsAffected())

	// Keyed on the address, not the account — see the read at the top.
	if email != nil && *email != "" {
		if _, err := tx.Exec(ctx,
			`DELETE FROM signup_tokens WHERE email = $1`, *email); err != nil {
			return nil, err
		}
	}

	// The anonymisation itself. Every column that identifies a person goes;
	// status and deleted_at mark what is left as a tombstone rather than a
	// customer, and token_epoch moves so any access token still in the wild
	// is refused on the permission-gated routes.
	//
	// primary_phone and primary_email are nulled rather than scrambled, which
	// also releases their UNIQUE constraints: somebody who deletes an account
	// and later signs up again with the same number gets a clean new one
	// instead of a collision they cannot see or resolve.
	if _, err := tx.Exec(ctx,
		`UPDATE accounts SET
		     status            = $2,
		     deleted_at        = now(),
		     primary_email     = NULL,
		     primary_phone     = NULL,
		     first_name        = NULL,
		     last_name         = NULL,
		     date_of_birth     = NULL,
		     profile_completed = false,
		     token_epoch       = token_epoch + 1,
		     updated_at        = now()
		  WHERE id = $1`, accountID, models.AccountDeleted); err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return res, nil
}

// deleteReturningURIs runs a DELETE ... RETURNING <uri column>, appending the
// non-empty results to acc. Shared by the two object-backed tables so the
// scan-and-close dance is written once; a leaked rows cursor inside a
// transaction wedges the whole purge.
func deleteReturningURIs(
	ctx context.Context, tx pgx.Tx, stmt string, accountID int64, acc []string,
) ([]string, int, error) {
	rows, err := tx.Query(ctx, stmt, accountID)
	if err != nil {
		return acc, 0, err
	}
	defer rows.Close()

	count := 0
	for rows.Next() {
		var uri *string
		if err := rows.Scan(&uri); err != nil {
			return acc, count, err
		}
		count++
		if uri != nil && *uri != "" {
			acc = append(acc, *uri)
		}
	}
	return acc, count, rows.Err()
}
