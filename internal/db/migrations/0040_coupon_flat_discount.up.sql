-- Flat-amount discount coupons ("₹2,000 off Loan Assist") beside the
-- percentage ones. A discount coupon is exactly one of the two: a percentage
-- with no flat amount, or a flat amount (rupees) with a zero percentage. A
-- referral code still discounts nothing.
--
-- Orders and coupon_redemptions already snapshot the rupee discount they were
-- priced with, so nothing downstream needs to know which kind it was.
ALTER TABLE coupons
    ADD COLUMN IF NOT EXISTS discount_flat NUMERIC(12,2) NOT NULL DEFAULT 0;

ALTER TABLE coupons DROP CONSTRAINT IF EXISTS coupons_discount_shape;
ALTER TABLE coupons ADD CONSTRAINT coupons_discount_shape CHECK (
    kind <> 'discount'
    OR (discount_percent > 0 AND discount_percent <= 100 AND discount_flat = 0)
    OR (discount_percent = 0 AND discount_flat > 0));

ALTER TABLE coupons DROP CONSTRAINT IF EXISTS coupons_referral_shape;
ALTER TABLE coupons ADD CONSTRAINT coupons_referral_shape CHECK (
    kind <> 'referral' OR (discount_percent = 0
                           AND discount_flat = 0
                           AND product_code IS NULL
                           AND max_redemptions IS NULL
                           AND valid_until IS NULL));
