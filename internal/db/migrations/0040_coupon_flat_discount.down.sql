-- Flat coupons cannot be expressed as percentages; revoke-by-delete is not an
-- option (redemptions reference them), so the down migration refuses if any exist.
DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM coupons WHERE discount_flat > 0) THEN
        RAISE EXCEPTION 'flat-amount coupons exist; cannot drop discount_flat';
    END IF;
END $$;
ALTER TABLE coupons DROP CONSTRAINT IF EXISTS coupons_discount_shape;
ALTER TABLE coupons ADD CONSTRAINT coupons_discount_shape CHECK (
    kind <> 'discount' OR (discount_percent > 0 AND discount_percent <= 100));
ALTER TABLE coupons DROP CONSTRAINT IF EXISTS coupons_referral_shape;
ALTER TABLE coupons ADD CONSTRAINT coupons_referral_shape CHECK (
    kind <> 'referral' OR (discount_percent = 0 AND product_code IS NULL
                           AND max_redemptions IS NULL AND valid_until IS NULL));
ALTER TABLE coupons DROP COLUMN IF EXISTS discount_flat;
