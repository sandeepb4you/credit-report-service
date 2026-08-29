-- Server-side entitlement for the paid bureau pull.
--
-- Until now POST /api/credit-analytics/request was authenticated but not paid.
-- The paywall lived entirely in the app, so any holder of a valid access token
-- could pull a bureau report — a call we are billed for — without ever buying
-- one, and the server had no way to tell a paid pull from a free one.
--
-- consumed_at marks a purchase as SPENT. It is deliberately not fulfilled_at,
-- which MarkOrderPaid stamps the moment Cashfree confirms payment and which
-- records that the entitlement was GRANTED. Granted and spent are different
-- events, and the window between them is the entire point of this column: a
-- user whose payment succeeded and whose pull then failed still holds an
-- unspent check, and must not be asked to pay twice for our vendor's outage.
--
-- consumed_report_id names the report the purchase actually bought. "What did
-- this payment get me" and "was this report paid for" are both questions a
-- refund request asks months later, and neither is answerable from timestamps
-- alone once an account has more than one of each.
ALTER TABLE orders
    ADD COLUMN IF NOT EXISTS consumed_at        TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS consumed_report_id BIGINT
        REFERENCES credit_analytics_requests (id) ON DELETE SET NULL;

COMMENT ON COLUMN orders.consumed_at IS
    'When the purchase was spent on a delivered report; NULL = still owed. Distinct from fulfilled_at, which records when payment granted it.';

-- Supports the only two queries that read this: "does this account hold an
-- unspent order of this product" and the claim that follows it.
CREATE INDEX IF NOT EXISTS idx_orders_entitlement
    ON orders (account_id, product_code, paid_at)
    WHERE status = 'PAID' AND consumed_at IS NULL;

-- Backfill. Without it, switching the gate on hands a free bureau pull to every
-- account that has ever paid, because every existing order has consumed_at NULL.
--
-- The rule applied here is the one the app was already using to answer the same
-- question client-side (HomeViewModel.checkUnconsumedScoreCheck): a paid check
-- counts as spent once a successful report exists that was created after it was
-- paid for. Reusing it means no existing user's entitlement changes at the
-- moment of migration — everyone keeps exactly what the client was already
-- showing them, and nobody is billed or blocked by the switch itself.
--
-- Orders and reports are paired one-for-one, oldest to oldest, rather than
-- marking every order spent when any later report exists. An account that
-- bought two checks and ran one pull genuinely has one left, and the cruder
-- rule would silently confiscate it.
WITH ranked_orders AS (
    SELECT id, account_id, paid_at,
           row_number() OVER (PARTITION BY account_id ORDER BY paid_at, id) AS rn
    FROM orders
    WHERE status = 'PAID'
      AND product_code = 'CREDIT_ANALYSIS'
      AND paid_at IS NOT NULL
),
ranked_reports AS (
    -- Successful pulls only, the same predicate the reports list and
    -- FindLatestByAccount use. A failed pull delivered nothing, so it cannot
    -- have spent anything.
    SELECT id, account_id, created_at,
           row_number() OVER (PARTITION BY account_id ORDER BY created_at, id) AS rn
    FROM credit_analytics_requests
    WHERE account_id IS NOT NULL
      AND http_status >= 200 AND http_status < 300
      AND response_body IS NOT NULL
)
UPDATE orders o
SET consumed_at        = rr.created_at,
    consumed_report_id = rr.id
FROM ranked_orders ro
JOIN ranked_reports rr
  ON rr.account_id = ro.account_id
 AND rr.rn = ro.rn
WHERE o.id = ro.id
  -- A report that predates the payment cannot have been bought by it. Those
  -- exist: every pull before this migration ran ungated. Leaving such an order
  -- unspent errs toward the user, which is the right direction to err.
  AND rr.created_at >= ro.paid_at;
