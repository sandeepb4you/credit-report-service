-- Restore the one-time check's pre-marketing name and checklist.
UPDATE products SET
    name        = 'Credit Report Analysis',
    description = E'Experian credit score (live pull)\nFull credit report — all accounts & enquiries\nPersonalised improvement plan\nLoan & credit card eligibility\nPDF via email + WhatsApp',
    updated_at  = now()
WHERE code = 'CREDIT_ANALYSIS';

-- Orders may reference the plan codes, so deactivate rather than delete when
-- they have been sold; delete outright only when nothing references them.
DELETE FROM products
 WHERE code IN ('SCORE_PLUS_QUARTERLY', 'SCORE_PLUS_MONTHLY')
   AND NOT EXISTS (SELECT 1 FROM orders o WHERE o.product_code = products.code);
UPDATE products SET active = FALSE
 WHERE code IN ('SCORE_PLUS_QUARTERLY', 'SCORE_PLUS_MONTHLY');

ALTER TABLE products
    DROP COLUMN IF EXISTS checks_included,
    DROP COLUMN IF EXISTS interval_months,
    DROP COLUMN IF EXISTS validity_days;
