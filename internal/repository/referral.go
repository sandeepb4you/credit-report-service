package repository

import (
	"context"
	"time"

	"github.com/georgysavva/scany/v2/pgxscan"
	"github.com/jackc/pgx/v5/pgxpool"

	"credit-report-service/internal/models"
)

// ReferralRepo reads the referral graph for the admin report.
//
// Read-only by design: attribution is written by the signup paths (see
// AccountRepo.CreateAccount, which sets referred_at in the same INSERT), and
// nothing here should be able to move a signup from one referrer to another.
type ReferralRepo struct{ pool *pgxpool.Pool }

func NewReferralRepo(pool *pgxpool.Pool) *ReferralRepo { return &ReferralRepo{pool: pool} }

// CountReferred is the number of attributed signups in [from, to).
func (r *ReferralRepo) CountReferred(ctx context.Context, from, to time.Time) (int, error) {
	var n int
	err := pgxscan.Get(ctx, r.pool, &n,
		`SELECT COUNT(*) FROM accounts
		  WHERE referred_by_account_id IS NOT NULL
		    AND referred_at >= $1 AND referred_at < $2`,
		from, to)
	return n, err
}

// TopReferrers is the leaderboard for the window, busiest first.
//
// The join onto coupons is a LEFT JOIN because a referrer's code is minted
// lazily, on first read of GET /coupons/referral. An agent who shared a code,
// had it revoked, and never re-read it still has referrals to their name and
// no live code — dropping those rows would silently under-report the period.
func (r *ReferralRepo) TopReferrers(
	ctx context.Context, from, to time.Time, limit int,
) ([]models.ReferrerSummary, error) {
	var out []models.ReferrerSummary
	err := pgxscan.Select(ctx, r.pool, &out,
		`SELECT ref.id                                              AS account_id,
		        TRIM(BOTH ' ' FROM COALESCE(ref.first_name, '') || ' ' ||
		                           COALESCE(ref.last_name, ''))     AS name,
		        ref.primary_phone                                   AS phone,
		        ref.primary_email                                   AS email,
		        code.code                                           AS referral_code,
		        COUNT(a.id)                                         AS referred_count
		   FROM accounts a
		   JOIN accounts ref ON ref.id = a.referred_by_account_id
		   LEFT JOIN coupons code
		          ON code.created_by = ref.id
		         AND code.kind = 'referral'
		         AND code.revoked_at IS NULL
		  WHERE a.referred_at >= $1 AND a.referred_at < $2
		  GROUP BY ref.id, ref.first_name, ref.last_name,
		           ref.primary_phone, ref.primary_email, code.code
		  ORDER BY referred_count DESC, ref.id
		  LIMIT $3`,
		from, to, limit)
	if out == nil {
		out = []models.ReferrerSummary{}
	}
	return out, err
}

// ListReferred pages the individual signups in the window, newest first.
// A non-nil referrerID narrows it to one referrer's recruits.
func (r *ReferralRepo) ListReferred(
	ctx context.Context, from, to time.Time, referrerID *int64, limit, offset int,
) ([]models.ReferredAccount, int, error) {
	var total int
	if err := pgxscan.Get(ctx, r.pool, &total,
		`SELECT COUNT(*) FROM accounts
		  WHERE referred_by_account_id IS NOT NULL
		    AND referred_at >= $1 AND referred_at < $2
		    AND ($3::bigint IS NULL OR referred_by_account_id = $3)`,
		from, to, referrerID); err != nil {
		return nil, 0, err
	}

	var out []models.ReferredAccount
	err := pgxscan.Select(ctx, r.pool, &out,
		`SELECT a.id                                                AS account_id,
		        TRIM(BOTH ' ' FROM COALESCE(a.first_name, '') || ' ' ||
		                           COALESCE(a.last_name, ''))       AS name,
		        a.primary_phone                                     AS phone,
		        a.primary_email                                     AS email,
		        a.status                                            AS status,
		        a.profile_completed                                 AS profile_completed,
		        a.referred_by_account_id                            AS referred_by_account_id,
		        TRIM(BOTH ' ' FROM COALESCE(ref.first_name, '') || ' ' ||
		                           COALESCE(ref.last_name, ''))     AS referred_by_name,
		        COALESCE(a.referred_by_code, '')                    AS referred_by_code,
		        a.referred_at                                       AS referred_at
		   FROM accounts a
		   JOIN accounts ref ON ref.id = a.referred_by_account_id
		  WHERE a.referred_at >= $1 AND a.referred_at < $2
		    AND ($3::bigint IS NULL OR a.referred_by_account_id = $3)
		  ORDER BY a.referred_at DESC, a.id DESC
		  LIMIT $4 OFFSET $5`,
		from, to, referrerID, limit, offset)
	if out == nil {
		out = []models.ReferredAccount{}
	}
	return out, total, err
}
