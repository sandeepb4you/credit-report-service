package repository

import (
	"context"
	"errors"

	"github.com/georgysavva/scany/v2/pgxscan"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"credit-report-service/internal/models"
)

// CouponRepo is the data access layer for coupons and their redemptions.
type CouponRepo struct{ pool *pgxpool.Pool }

func NewCouponRepo(pool *pgxpool.Pool) *CouponRepo { return &CouponRepo{pool: pool} }

// BeginTx starts a transaction so the service layer can create an order and
// claim a coupon atomically without importing pgxpool.
func (r *CouponRepo) BeginTx(ctx context.Context) (pgx.Tx, error) { return r.pool.Begin(ctx) }

const couponCols = `id, kind, code, created_by, discount_percent, product_code,
    max_redemptions, redemption_count, per_account_limit,
    valid_from, valid_until, revoked_at, created_at, updated_at`

func (r *CouponRepo) Create(ctx context.Context, c *models.Coupon) error {
	err := pgxscan.Get(ctx, r.pool, c,
		`INSERT INTO coupons
		     (kind, code, created_by, discount_percent, product_code,
		      max_redemptions, per_account_limit, valid_from, valid_until)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		 RETURNING `+couponCols,
		c.Kind, c.Code, c.CreatedBy, c.DiscountPercent, c.ProductCode,
		c.MaxRedemptions, c.PerAccountLimit, c.ValidFrom, c.ValidUntil)
	return classifyPgErr(err)
}

// FindLiveReferralByOwner returns an account's referral code, if it has one.
func (r *CouponRepo) FindLiveReferralByOwner(ctx context.Context, accountID int64) (*models.Coupon, error) {
	var c models.Coupon
	err := pgxscan.Get(ctx, r.pool, &c,
		`SELECT `+couponCols+` FROM coupons
		  WHERE created_by = $1 AND kind = 'referral' AND revoked_at IS NULL`,
		accountID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &c, err
}

// FindLiveReferralByCode resolves a referral code to its owner. Referral codes
// have no expiry or cap, so "live" means only "not revoked".
func (r *CouponRepo) FindLiveReferralByCode(ctx context.Context, code string) (*models.Coupon, error) {
	var c models.Coupon
	err := pgxscan.Get(ctx, r.pool, &c,
		`SELECT `+couponCols+` FROM coupons
		  WHERE code = $1 AND kind = 'referral' AND revoked_at IS NULL`,
		code)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &c, err
}

func (r *CouponRepo) FindByCode(ctx context.Context, code string) (*models.Coupon, error) {
	var c models.Coupon
	err := pgxscan.Get(ctx, r.pool, &c,
		`SELECT `+couponCols+` FROM coupons WHERE code = $1`, code)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &c, err
}

// ListByCreator returns the coupons one account issued, newest first.
func (r *CouponRepo) ListByCreator(ctx context.Context, accountID int64) ([]models.Coupon, error) {
	var out []models.Coupon
	err := pgxscan.Select(ctx, r.pool, &out,
		`SELECT `+couponCols+` FROM coupons WHERE created_by = $1 ORDER BY created_at DESC`,
		accountID)
	return out, err
}

// ListAll returns every coupon, newest first. For holders of coupon:admin.
func (r *CouponRepo) ListAll(ctx context.Context) ([]models.Coupon, error) {
	var out []models.Coupon
	err := pgxscan.Select(ctx, r.pool, &out,
		`SELECT `+couponCols+` FROM coupons ORDER BY created_at DESC`)
	return out, err
}

// Revoke deactivates a coupon. When onlyCreator is non-zero the update is
// scoped to that issuer, so an agent can only revoke its own — a coupon
// belonging to someone else is reported as missing rather than forbidden.
// Existing redemptions are left intact; revoking stops future use only.
func (r *CouponRepo) Revoke(ctx context.Context, code string, onlyCreator int64) (bool, error) {
	tag, err := r.pool.Exec(ctx,
		`UPDATE coupons SET revoked_at = now(), updated_at = now()
		  WHERE code = $1 AND revoked_at IS NULL
		    AND ($2 = 0 OR created_by = $2)`,
		code, onlyCreator)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// ---- redemption ----------------------------------------------------------

// Claim atomically reserves one redemption of a coupon inside tx.
//
// Validity window, revocation, and the redemption cap are all evaluated in the
// WHERE clause of a single conditional UPDATE, so concurrent checkouts cannot
// both pass a check-then-increment race and push the count past the cap. A
// read-then-write here would be wrong no matter how tight the window.
//
// It also takes a row lock on the coupon for the rest of the transaction,
// which is what makes the subsequent per-account count safe: every other
// redemption of this coupon blocks until this transaction ends.
//
// Returns ErrNotFound when the coupon is unknown, revoked, outside its window,
// exhausted, or is a referral code rather than a discount one — the caller
// cannot distinguish these, and should not, since probing which is which leaks
// the state of other people's coupons.
func (r *CouponRepo) Claim(ctx context.Context, tx pgx.Tx, code string) (*models.Coupon, error) {
	var c models.Coupon
	err := pgxscan.Get(ctx, tx, &c,
		`UPDATE coupons
		    SET redemption_count = redemption_count + 1,
		        updated_at = now()
		  WHERE code = $1
		    AND kind = 'discount'
		    AND revoked_at IS NULL
		    AND now() >= valid_from
		    AND (valid_until IS NULL OR now() < valid_until)
		    AND (max_redemptions IS NULL OR redemption_count < max_redemptions)
		RETURNING `+couponCols,
		code)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &c, err
}

// CountAccountRedemptions returns how many live (unreleased) redemptions an
// account holds for a coupon. Safe against races only when called inside the
// transaction that already claimed the coupon — see Claim.
func (r *CouponRepo) CountAccountRedemptions(
	ctx context.Context, tx pgx.Tx, couponID, accountID int64,
) (int, error) {
	var n int
	err := tx.QueryRow(ctx,
		`SELECT count(*) FROM coupon_redemptions
		  WHERE coupon_id = $1 AND account_id = $2 AND released_at IS NULL`,
		couponID, accountID).Scan(&n)
	return n, err
}

// RecordRedemption writes the redemption row inside tx.
func (r *CouponRepo) RecordRedemption(
	ctx context.Context, tx pgx.Tx, red *models.CouponRedemption,
) error {
	err := tx.QueryRow(ctx,
		`INSERT INTO coupon_redemptions (coupon_id, account_id, order_uid, discount_amount)
		 VALUES ($1, $2, $3, $4)
		 RETURNING id, redeemed_at`,
		red.CouponID, red.AccountID, red.OrderUID, red.DiscountAmount,
	).Scan(&red.ID, &red.RedeemedAt)
	return classifyPgErr(err)
}

// ReleaseByOrder gives a redemption back when its order will never be paid.
//
// Marking the row released and decrementing the counter happen together, and
// the released_at IS NULL guard makes it idempotent: a failed order that is
// reconciled twice, or fails and then expires, releases exactly once.
func (r *CouponRepo) ReleaseByOrder(ctx context.Context, orderUID string) (bool, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)

	var couponID int64
	err = tx.QueryRow(ctx,
		`UPDATE coupon_redemptions SET released_at = now()
		  WHERE order_uid = $1 AND released_at IS NULL
		RETURNING coupon_id`, orderUID).Scan(&couponID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil // no coupon on this order, or already released
	}
	if err != nil {
		return false, err
	}

	// GREATEST guards the check constraint if a counter ever drifts below the
	// number of live redemptions.
	if _, err := tx.Exec(ctx,
		`UPDATE coupons SET redemption_count = GREATEST(redemption_count - 1, 0),
		        updated_at = now()
		  WHERE id = $1`, couponID); err != nil {
		return false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return true, nil
}

// CountRedemptions returns total and released counts for a coupon, for the
// issuer's reporting view.
func (r *CouponRepo) CountRedemptions(ctx context.Context, couponID int64) (live, released int, err error) {
	err = r.pool.QueryRow(ctx,
		`SELECT count(*) FILTER (WHERE released_at IS NULL),
		        count(*) FILTER (WHERE released_at IS NOT NULL)
		   FROM coupon_redemptions WHERE coupon_id = $1`, couponID).Scan(&live, &released)
	return live, released, err
}
