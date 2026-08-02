package models

import (
	"math"
	"time"
)

// Coupon kinds. Both share the coupons table, and therefore one code
// namespace, so a referral code can never collide with a discount code.
const (
	// CouponDiscount is spent at checkout for a percentage off, once per
	// account, optionally capped and time-boxed.
	CouponDiscount = "discount"
	// CouponReferral is a permanent code identifying its owner. It is consumed
	// at signup to attribute the new account, carries no discount, and never
	// expires or runs out.
	CouponReferral = "referral"
)

// Coupon code bounds. Codes are stored upper-cased so lookups are exact.
const (
	CouponCodeMinLen = 3
	CouponCodeMaxLen = 32
)

// ReferralCodePrefix marks generated referral codes so a user can tell at a
// glance which kind of code they are holding.
const ReferralCodePrefix = "REF"

// MinChargeableAmount is the smallest total the payment gateway will accept.
// A coupon that discounts an order below this is rejected at checkout rather
// than silently clamped, because clamping would overcharge against a discount
// the customer was shown.
const MinChargeableAmount = 0

// Coupon is the row model for the coupons table: a percentage discount issued
// by an agent or admin.
type Coupon struct {
	ID   int64  `json:"id"   db:"id"`
	Kind string `json:"kind" db:"kind"`
	Code string `json:"code" db:"code"`
	// CreatedBy is the issuing account — the agent this redemption attributes to.
	CreatedBy       int64   `json:"createdBy"       db:"created_by"`
	DiscountPercent float64 `json:"discountPercent" db:"discount_percent"`
	// ProductCode nil means the coupon applies to every product.
	ProductCode *string `json:"productCode"     db:"product_code"`

	MaxRedemptions  *int `json:"maxRedemptions"  db:"max_redemptions"`
	RedemptionCount int  `json:"redemptionCount" db:"redemption_count"`
	PerAccountLimit int  `json:"perAccountLimit" db:"per_account_limit"`

	ValidFrom  time.Time  `json:"validFrom"  db:"valid_from"`
	ValidUntil *time.Time `json:"validUntil" db:"valid_until"`
	RevokedAt  *time.Time `json:"revokedAt"  db:"revoked_at"`

	CreatedAt time.Time `json:"createdAt" db:"created_at"`
	UpdatedAt time.Time `json:"updatedAt" db:"updated_at"`
}

// CouponRedemption is the row model for coupon_redemptions: one per order that
// applied a coupon.
type CouponRedemption struct {
	ID             int64      `json:"id"             db:"id"`
	CouponID       int64      `json:"couponId"       db:"coupon_id"`
	AccountID      int64      `json:"accountId"      db:"account_id"`
	OrderUID       string     `json:"orderId"        db:"order_uid"`
	DiscountAmount float64    `json:"discountAmount" db:"discount_amount"`
	RedeemedAt     time.Time  `json:"redeemedAt"     db:"redeemed_at"`
	ReleasedAt     *time.Time `json:"releasedAt"     db:"released_at"`
}

// IsReferral reports whether this is a signup-attribution code rather than a
// checkout discount.
func (c *Coupon) IsReferral() bool { return c.Kind == CouponReferral }

// AppliesTo reports whether the coupon can be used for a product. A referral
// code is never spendable at checkout; a discount coupon with no product scope
// applies to all of them.
func (c *Coupon) AppliesTo(productCode string) bool {
	if c.IsReferral() {
		return false
	}
	return c.ProductCode == nil || *c.ProductCode == productCode
}

// ApplyTo computes the discount and the resulting payable amount for a price.
//
// This is the only place a discounted price is derived. The client never sends
// an amount — it sends a coupon code, and the price it is shown comes from
// here — so a tampered request cannot change what is charged.
//
// Rounding is to paise, and the discount is clamped at the full price so a
// 100% coupon can never produce a negative total.
func (c *Coupon) ApplyTo(amount float64) (discount, payable float64) {
	if amount <= 0 {
		return 0, 0
	}
	// amount*percent/100 rounded to 2dp: scale by 100 to round in paise, which
	// is the same as rounding amount*percent to the nearest whole unit.
	discount = math.Round(amount*c.DiscountPercent) / 100
	if discount > amount {
		discount = amount
	}
	payable = math.Round((amount-discount)*100) / 100
	return discount, payable
}

// UsableAt reports whether the coupon is live at a point in time: not revoked
// and inside its validity window. It deliberately does not consider redemption
// counts — those are checked atomically at claim time, because any count read
// outside that UPDATE is stale the moment it is taken.
func (c *Coupon) UsableAt(t time.Time) bool {
	if c.RevokedAt != nil {
		return false
	}
	if t.Before(c.ValidFrom) {
		return false
	}
	if c.ValidUntil != nil && !t.Before(*c.ValidUntil) {
		return false
	}
	return true
}

// Exhausted reports whether the coupon has hit its cap. Advisory only — for
// display and early rejection; the authoritative check is the conditional
// UPDATE in CouponRepo.Claim.
func (c *Coupon) Exhausted() bool {
	return c.MaxRedemptions != nil && c.RedemptionCount >= *c.MaxRedemptions
}
