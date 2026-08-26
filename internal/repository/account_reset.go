package repository

import (
	"context"
	"errors"

	"credit-report-service/internal/models"
	"github.com/jackc/pgx/v5"
)

// Package repository — putting an account back to the state it had just after
// signup, so the onboarding funnel can be walked again with the same login.
//
// This is a testing tool with production teeth: it deletes reports somebody paid
// for. The service layer, not this file, is where the confirmation and the audit
// line live; here the only job is to do the whole wipe in one transaction, so a
// failure halfway cannot leave an account with its PAN gone but its reports
// still standing.

// SignupResetPreview counts what a reset would remove, without removing it.
//
// Deliberately a separate read rather than a dry-run of the delete: an admin
// looking up an account is the common case, and it must not take row locks on
// somebody's live data to answer.
func (r *AccountRepo) SignupResetPreview(
	ctx context.Context, accountID int64,
) (*models.AccountResetCounts, error) {
	return signupResetPreview(ctx, r.pool, accountID)
}

// signupResetPreview runs the count against a pool or a transaction, so the
// receipt a reset returns is counted inside the same transaction that deletes
// the rows rather than a moment before it.
func signupResetPreview(
	ctx context.Context, q querier, accountID int64,
) (*models.AccountResetCounts, error) {
	var c models.AccountResetCounts
	err := q.QueryRow(ctx,
		`SELECT
		   (SELECT count(*) FROM credit_analytics_requests WHERE account_id = $1),
		   (SELECT count(*) FROM orders                    WHERE account_id = $1),
		   (SELECT count(*) FROM orders                    WHERE account_id = $1 AND status = $2),
		   (SELECT count(*) FROM bank_statements           WHERE account_id = $1),
		   (SELECT count(*) FROM coupon_redemptions        WHERE account_id = $1),
		   (SELECT count(*) FROM prefill_lookups           WHERE account_id = $1),
		   (SELECT count(*) FROM otp_challenges            WHERE account_id = $1),
		   (SELECT count(*) FROM sessions                  WHERE account_id = $1 AND revoked_at IS NULL),
		   (SELECT count(*) FROM kyc_records               WHERE account_id = $1) > 0,
		   COALESCE(NULLIF(a.first_name, ''), NULLIF(a.last_name, '')) IS NOT NULL,
		   a.date_of_birth IS NOT NULL,
		   (a.referred_by_account_id IS NOT NULL OR a.referred_by_code IS NOT NULL)
		 FROM accounts a WHERE a.id = $1`,
		accountID, models.OrderPaid,
	).Scan(
		&c.Reports, &c.Orders, &c.PaidOrders, &c.BankStatements,
		&c.CouponRedemptions, &c.PrefillLookups, &c.OTPChallenges, &c.ActiveSessions,
		&c.HasKYCRecord, &c.HasProfileName, &c.HasDateOfBirth, &c.HasReferralCredit,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &c, err
}

// ResetToSignup deletes everything the account did after signing up and clears
// the profile fields, in one transaction.
//
// What survives is deliberate: the accounts row, its role, and its
// auth_identities. The login is the thing being kept — the point is to walk the
// same phone number or address back through onboarding, not to free it up. Role
// is untouched because a reset is not a demotion, and an admin resetting their
// own account should still be an admin when they sign back in.
//
// Sessions are revoked rather than deleted, matching every other revocation
// path (the rows are the forensic record of a session, and the reset itself is
// already logged as the reason). token_epoch moves in the same statement, which
// is what kills access tokens already in the wild — without it the target would
// keep working against a wiped account until its current token expired.
func (r *AccountRepo) ResetToSignup(
	ctx context.Context, accountID int64,
) (*models.AccountResetResult, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	before, err := signupResetPreview(ctx, tx, accountID)
	if err != nil {
		return nil, err
	}

	// Reports first, keeping the object URIs: the rows are about to go, and the
	// encrypted PDFs in the bucket have to follow them out.
	rows, err := tx.Query(ctx,
		`DELETE FROM credit_analytics_requests
		  WHERE account_id = $1
		 RETURNING result_pdf_url`, accountID)
	if err != nil {
		return nil, err
	}
	var pdfURIs []string
	for rows.Next() {
		var uri *string
		if err := rows.Scan(&uri); err != nil {
			rows.Close()
			return nil, err
		}
		if uri != nil && *uri != "" {
			pdfURIs = append(pdfURIs, *uri)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Coupon redemptions before orders: the redemption references order_uid, and
	// clearing it is also what lets the same coupon be tested again.
	for _, stmt := range []string{
		`DELETE FROM coupon_redemptions WHERE account_id = $1`,
		`DELETE FROM payment_webhook_events
		  WHERE order_uid IN (SELECT order_uid FROM orders WHERE account_id = $1)`,
		`DELETE FROM orders WHERE account_id = $1`,
		`DELETE FROM kyc_records WHERE account_id = $1`,
		`DELETE FROM prefill_lookups WHERE account_id = $1`,
		`DELETE FROM bank_statements WHERE account_id = $1`,
		// Cooldowns and send caps live on the challenge rows, so leaving them
		// would make the next OTP screen refuse to send for 30 seconds.
		`DELETE FROM otp_challenges WHERE account_id = $1`,
		`DELETE FROM password_reset_tokens WHERE account_id = $1`,
		`UPDATE sessions SET revoked_at = now(), revoked_reason = 'account reset to signup'
		  WHERE account_id = $1 AND revoked_at IS NULL`,
	} {
		if _, err := tx.Exec(ctx, stmt, accountID); err != nil {
			return nil, err
		}
	}

	var epoch int
	err = tx.QueryRow(ctx,
		`UPDATE accounts SET
		     first_name             = NULL,
		     last_name              = NULL,
		     date_of_birth          = NULL,
		     profile_completed      = false,
		     referred_by_account_id = NULL,
		     referred_by_code       = NULL,
		     referred_at            = NULL,
		     token_epoch            = token_epoch + 1,
		     updated_at             = now()
		  WHERE id = $1
		 RETURNING token_epoch`, accountID).Scan(&epoch)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &models.AccountResetResult{
		AccountID:     accountID,
		Removed:       *before,
		PDFObjectURIs: pdfURIs,
		TokenEpoch:    epoch,
	}, nil
}
