-- Plan metadata on the product catalog, for the myScorr Plus plans.
--
-- The marketing site sells three plans: a one-time check, and two prepaid
-- yearly batches (quarterly = 4 checks, monthly = 12). These are NOT
-- subscriptions — one Cashfree payment, no mandate, no renewal. What the
-- interval fields describe is how fulfilment turns the single payment into a
-- batch of scheduled runs (see 0023_scheduled_checks).
--
-- checks_included defaults to 1 so every existing product keeps meaning what
-- it always meant: one payment, one check. interval_months NULL marks a
-- one-time product; fulfilment only mints scheduled runs when it is set.
ALTER TABLE products
    ADD COLUMN IF NOT EXISTS checks_included INT NOT NULL DEFAULT 1,
    ADD COLUMN IF NOT EXISTS interval_months INT,
    ADD COLUMN IF NOT EXISTS validity_days   INT;

COMMENT ON COLUMN products.checks_included IS
    'Score checks one purchase buys. 1 for one-time products.';
COMMENT ON COLUMN products.interval_months IS
    'Months between scheduled checks; NULL = one-time product, no schedule.';
COMMENT ON COLUMN products.validity_days IS
    'Days from payment until unrun scheduled checks expire; NULL = never.';

-- Prices and copy mirror the marketing home page (webApp/marketing/index.html,
-- pricing section). Description lines render as the app''s feature checklist.
INSERT INTO products (code, name, amount, currency, active, description,
                      checks_included, interval_months, validity_days)
VALUES
    ('SCORE_PLUS_QUARTERLY', 'myScorr Plus Quarterly', 699.00, 'INR', TRUE,
     E'Everything in myScorr Starter Plan\nScore refresh every 3 months\nChange alerts after every refresh\nScore history & trend',
     4, 3, 365),
    ('SCORE_PLUS_MONTHLY', 'myScorr Plus Monthly', 999.00, 'INR', TRUE,
     E'Everything in Quarterly\nScore refresh every month\nFull score history & trend graph\nCheapest per refresh',
     12, 1, 365)
ON CONFLICT (code) DO NOTHING;

-- The one-time check predates the marketing site and still carried its working
-- name. Rename it to what the home page sells (the Plus checklists say
-- "Everything in myScorr Starter Plan", so the name must exist on screen), and
-- align its checklist with the Starter card — which also drops the
-- "PDF via email + WhatsApp" line: WhatsApp delivery is not wired, and the
-- plans screen must not promise what the delivery screen shows disabled.
UPDATE products SET
    name        = 'myScorr Starter Plan',
    description = E'Live Experian score pull\nFull report, all accounts & enquiries\nAI-ranked improvement plan\nLoan & credit card eligibility',
    updated_at  = now()
WHERE code = 'CREDIT_ANALYSIS';
