-- Take the two statement plans out of the catalog entirely.
--
-- Only the credit report is being sold. The statement-analysis features still
-- exist in the app, but nothing gates them on a purchase — the product codes are
-- declared in models/order.go and referenced by no service — so removing the
-- plans breaks no flow, it only stops them being offered.
--
-- Safe to DELETE rather than deactivate because nothing points at them: checked
-- against production before writing this, both had zero orders and zero coupons.
-- The guards below re-check that at apply time rather than trusting it, because
-- another environment may have data this one does not, and orders.product_code
-- is a foreign key — a plan someone has actually bought must never vanish out
-- from under their order history.
--
-- Deactivating (products.active = false) remains the reversible option and is
-- what the admin screen does. This migration is the deliberate, reviewable form
-- of the same decision, applied identically to every environment.
DELETE FROM products
WHERE code IN ('BANK_STATEMENT_ANALYSIS', 'UPI_STATEMENT_ANALYSIS')
  AND NOT EXISTS (SELECT 1 FROM orders  o WHERE o.product_code = products.code)
  AND NOT EXISTS (SELECT 1 FROM coupons c WHERE c.product_code = products.code);

-- Anything that survived the delete is referenced somewhere, so retire it
-- instead of leaving it on sale. A plan cannot be both un-deletable and offered.
UPDATE products SET active = false, updated_at = now()
WHERE code IN ('BANK_STATEMENT_ANALYSIS', 'UPI_STATEMENT_ANALYSIS');
