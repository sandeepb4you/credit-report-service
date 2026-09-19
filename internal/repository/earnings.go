package repository

import (
	"context"
	"errors"

	"github.com/georgysavva/scany/v2/pgxscan"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"credit-report-service/internal/models"
)

// EarningsRepo owns the referral-money tables from migration 0028:
// referral_earnings (one credit per referred account), payout_bank_accounts
// (one destination per user) and referral_withdrawals (the manual-payout
// queue). Attribution itself stays where it was — accounts.referred_by_* —
// and this repo never writes it.
type EarningsRepo struct{ pool *pgxpool.Pool }

func NewEarningsRepo(pool *pgxpool.Pool) *EarningsRepo { return &EarningsRepo{pool: pool} }

// GetSettings reads the programme economics. Falls back to the seeded
// defaults when the row is missing rather than failing the request —
// earnings must keep working even if the settings table is ever out of step.
func (r *EarningsRepo) GetSettings(ctx context.Context) (*models.ReferralSettings, error) {
	var out models.ReferralSettings
	err := pgxscan.Get(ctx, r.pool, &out,
		`SELECT reward_paise, min_withdrawal_paise FROM referral_settings WHERE id = 1`)
	if errors.Is(err, pgx.ErrNoRows) {
		return &models.ReferralSettings{
			RewardPaise:        models.ReferralRewardPaise,
			MinWithdrawalPaise: models.DefaultReferralMinWithdrawalPaise,
		}, nil
	}
	return &out, classifyPgErr(err)
}

// UpdateSettings changes the economics going forward (history keeps its
// snapshots). Either pointer may be nil to leave that value alone; returns
// the resolved row.
func (r *EarningsRepo) UpdateSettings(
	ctx context.Context, adminID int64, rewardPaise, minWithdrawalPaise *int,
) (*models.ReferralSettings, error) {
	var out models.ReferralSettings
	err := pgxscan.Get(ctx, r.pool, &out,
		`UPDATE referral_settings SET
		     reward_paise = COALESCE($2, reward_paise),
		     min_withdrawal_paise = COALESCE($3, min_withdrawal_paise),
		     updated_at = now(),
		     updated_by_account_id = $1
		 WHERE id = 1
		 RETURNING reward_paise, min_withdrawal_paise`,
		adminID, rewardPaise, minWithdrawalPaise)
	return &out, classifyPgErr(err)
}

// CreditForReferral records the flat reward for one referred account's first
// paid purchase. The UNIQUE on referred_account_id makes this safe to call on
// every PAID transition: only the first call inserts, the rest are no-ops.
// Returns true when this call created the credit.
func (r *EarningsRepo) CreditForReferral(
	ctx context.Context, referrerID, referredID int64, orderUID string, amountPaise int,
) (bool, error) {
	var id int64
	err := r.pool.QueryRow(ctx,
		`INSERT INTO referral_earnings
		     (referrer_account_id, referred_account_id, order_uid, amount_paise)
		 VALUES ($1, $2, $3, $4)
		 ON CONFLICT (referred_account_id) DO NOTHING
		 RETURNING id`,
		referrerID, referredID, orderUID, amountPaise,
	).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return err == nil, classifyPgErr(err)
}

// EarningsTotals is the arithmetic behind EarningsSummary: what the referrer
// earned in total, what left via paid withdrawals, what is locked in pending
// ones, and how many referrals converted vs are still waiting on a purchase.
type EarningsTotals struct {
	EarnedPaise     int
	PaidPaise       int
	PendingPaise    int
	SuccessfulCount int
	PendingCount    int
}

func (r *EarningsRepo) Totals(ctx context.Context, referrerID int64) (*EarningsTotals, error) {
	var t EarningsTotals
	err := r.pool.QueryRow(ctx,
		`SELECT
		   COALESCE((SELECT SUM(amount_paise) FROM referral_earnings
		              WHERE referrer_account_id = $1), 0),
		   COALESCE((SELECT SUM(amount_paise) FROM referral_withdrawals
		              WHERE account_id = $1 AND status = 'PAID'), 0),
		   COALESCE((SELECT SUM(amount_paise) FROM referral_withdrawals
		              WHERE account_id = $1 AND status = 'PENDING'), 0),
		   (SELECT COUNT(*) FROM referral_earnings
		     WHERE referrer_account_id = $1),
		   (SELECT COUNT(*) FROM accounts a
		     WHERE a.referred_by_account_id = $1
		       AND NOT EXISTS (SELECT 1 FROM referral_earnings e
		                        WHERE e.referred_account_id = a.id))`,
		referrerID,
	).Scan(&t.EarnedPaise, &t.PaidPaise, &t.PendingPaise,
		&t.SuccessfulCount, &t.PendingCount)
	return &t, classifyPgErr(err)
}

// ReferredRow is one signup through a referrer's code with the purchase signal
// attached. Raw contact values — masking happens in the service, so the admin
// queue can keep reading the same query shape without it.
type ReferredRow struct {
	Name          string  `db:"name"`
	Phone         *string `db:"phone"`
	Email         *string `db:"email"`
	ReferredAt    string  `db:"referred_at"`
	FirstPaidAt   *string `db:"first_paid_at"`
	CreditedPaise *int    `db:"credited_paise"`
}

// ListReferredBy lists every signup through referrerID, oldest first, with
// the referred account's first PAID purchase (any product) attached. A row
// with no FirstPaidAt is a pending referral; one with it is successful.
func (r *EarningsRepo) ListReferredBy(ctx context.Context, referrerID int64) ([]ReferredRow, error) {
	var out []ReferredRow
	err := pgxscan.Select(ctx, r.pool, &out,
		`SELECT TRIM(BOTH ' ' FROM COALESCE(a.first_name, '') || ' ' ||
		                            COALESCE(a.last_name, '')) AS name,
		        a.primary_phone AS phone,
		        a.primary_email AS email,
		        to_char(a.referred_at, 'YYYY-MM-DD') AS referred_at,
		        (SELECT to_char(MIN(o.paid_at), 'YYYY-MM-DD') FROM orders o
		          WHERE o.account_id = a.id AND o.status = 'PAID') AS first_paid_at,
		        e.amount_paise AS credited_paise
		   FROM accounts a
		   LEFT JOIN referral_earnings e ON e.referred_account_id = a.id
		  WHERE a.referred_by_account_id = $1
		  ORDER BY a.referred_at, a.id`,
		referrerID)
	if out == nil {
		out = []ReferredRow{}
	}
	return out, classifyPgErr(err)
}

// BankRow is the stored payout destination, full number included — for the
// owner snapshot and the admin queue only, never for a list response.
type BankRow struct {
	HolderName    string `db:"holder_name"`
	AccountNumber string `db:"account_number"`
	IFSC          string `db:"ifsc"`
}

// GetBank returns the caller's payout destination, or ErrNotFound when they
// have not saved one yet.
func (r *EarningsRepo) GetBank(ctx context.Context, accountID int64) (*BankRow, error) {
	var out BankRow
	err := pgxscan.Get(ctx, r.pool, &out,
		`SELECT holder_name, account_number, ifsc
		   FROM payout_bank_accounts WHERE account_id = $1`, accountID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &out, classifyPgErr(err)
}

// UpsertBank saves the caller's payout destination (one row per account).
func (r *EarningsRepo) UpsertBank(
	ctx context.Context, accountID int64, holder, number, ifsc string,
) error {
	_, err := r.pool.Exec(ctx,
		`INSERT INTO payout_bank_accounts (account_id, holder_name, account_number, ifsc)
		 VALUES ($1, $2, $3, $4)
		 ON CONFLICT (account_id) DO UPDATE SET
		     holder_name = EXCLUDED.holder_name,
		     account_number = EXCLUDED.account_number,
		     ifsc = EXCLUDED.ifsc,
		     updated_at = now()`,
		accountID, holder, number, ifsc)
	return classifyPgErr(err)
}

// CreateWithdrawal inserts a PENDING request with the bank snapshot. Balance
// validation lives in the service — by the time this runs the amount is known
// good, and the available-balance recheck happens in the same statement via
// the totals subquery to close the request-request race.
func (r *EarningsRepo) CreateWithdrawal(
	ctx context.Context, accountID int64, amountPaise int,
	holder, last4, ifsc string,
) (*models.Withdrawal, error) {
	var w models.Withdrawal
	err := pgxscan.Get(ctx, r.pool, &w,
		`INSERT INTO referral_withdrawals
		     (account_id, amount_paise, holder_name, account_last4, ifsc)
		 VALUES ($1, $2, $3, $4, $5)
		 RETURNING id, amount_paise, status, holder_name, account_last4, ifsc,
		           requested_at, decided_at, reject_reason`,
		accountID, amountPaise, holder, last4, ifsc)
	return &w, classifyPgErr(err)
}

// ListWithdrawalsMine returns the caller's own requests, newest first.
func (r *EarningsRepo) ListWithdrawalsMine(
	ctx context.Context, accountID int64,
) ([]models.Withdrawal, error) {
	var out []models.Withdrawal
	err := pgxscan.Select(ctx, r.pool, &out,
		`SELECT id, amount_paise, status, holder_name, account_last4, ifsc,
		        requested_at, decided_at, reject_reason
		   FROM referral_withdrawals
		  WHERE account_id = $1
		  ORDER BY requested_at DESC, id DESC`, accountID)
	if out == nil {
		out = []models.Withdrawal{}
	}
	return out, classifyPgErr(err)
}

// ListWithdrawalsForReview pages the manual-payout queue with the requester's
// identity and full bank destination attached. A nil status lists everything;
// otherwise only that status. Newest requests last — the queue is worked
// oldest-first.
func (r *EarningsRepo) ListWithdrawalsForReview(
	ctx context.Context, status *string, limit, offset int,
) ([]models.AdminWithdrawalItem, int, error) {
	var total int
	if err := pgxscan.Get(ctx, r.pool, &total,
		`SELECT COUNT(*) FROM referral_withdrawals w
		  WHERE ($1::text IS NULL OR w.status = $1)`, status); err != nil {
		return nil, 0, classifyPgErr(err)
	}
	var out []models.AdminWithdrawalItem
	err := pgxscan.Select(ctx, r.pool, &out,
		`SELECT w.id, w.account_id, w.amount_paise, w.status,
		        w.holder_name, w.account_last4, w.ifsc,
		        w.requested_at, w.decided_at, w.reject_reason,
		        TRIM(BOTH ' ' FROM COALESCE(a.first_name, '') || ' ' ||
		                           COALESCE(a.last_name, '')) AS user_name,
		        a.primary_phone AS user_phone,
		        a.primary_email AS user_email,
		        b.account_number AS account_number,
		        b.ifsc AS bank_ifsc,
		        b.holder_name AS bank_holder
		   FROM referral_withdrawals w
		   JOIN accounts a ON a.id = w.account_id
		   LEFT JOIN payout_bank_accounts b ON b.account_id = w.account_id
		  WHERE ($1::text IS NULL OR w.status = $1)
		  ORDER BY w.requested_at, w.id
		  LIMIT $2 OFFSET $3`, status, limit, offset)
	if out == nil {
		out = []models.AdminWithdrawalItem{}
	}
	return out, total, classifyPgErr(err)
}

// DecideWithdrawal flips a PENDING request to PAID or REJECTED. The status
// guard is what makes a double-click (or two admins) safe: only the first
// decision lands. Returns false when the request was not pending.
func (r *EarningsRepo) DecideWithdrawal(
	ctx context.Context, id int64, adminID int64, paid bool, reason *string,
) (bool, error) {
	status := models.WithdrawalRejected
	if paid {
		reason = nil
		status = models.WithdrawalPaid
	}
	tag, err := r.pool.Exec(ctx,
		`UPDATE referral_withdrawals SET
		     status = $2,
		     decided_at = now(),
		     decided_by_account_id = $3,
		     reject_reason = $4,
		     requested_at = requested_at
		 WHERE id = $1 AND status = 'PENDING'`,
		id, status, adminID, reason)
	if err != nil {
		return false, classifyPgErr(err)
	}
	return tag.RowsAffected() == 1, nil
}

// CountPaidOrders reports how many PAID orders an account holds. The earnings
// credit fires when this moves 0 → 1 — the first purchase, of any product —
// and never again.
func (r *OrderRepo) CountPaidOrders(ctx context.Context, accountID int64) (int, error) {
	var n int
	err := r.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM orders WHERE account_id = $1 AND status = $2`,
		accountID, models.OrderPaid).Scan(&n)
	return n, classifyPgErr(err)
}

// ReferredBy returns who attributed the account, if anyone. The earnings hook
// reads it at fulfilment time rather than trusting anything the order carries.
func (r *AccountRepo) ReferredBy(ctx context.Context, accountID int64) (*int64, error) {
	var out *int64
	err := r.pool.QueryRow(ctx,
		`SELECT referred_by_account_id FROM accounts WHERE id = $1`, accountID).Scan(&out)
	return out, classifyPgErr(err)
}
