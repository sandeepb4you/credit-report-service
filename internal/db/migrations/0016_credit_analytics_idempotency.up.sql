-- Idempotency for the bureau pull.
--
-- POST /api/credit-analytics/request is not safe to repeat: each call is billed
-- by Digitap and, since the entitlement gate landed, each one also SPENDS one of
-- the account's paid orders. The app runs the pull from a screen whose
-- ViewModel starts it on creation, so any recreation of that back-stack entry
-- re-fires it — cheap when the endpoint was free, now silently worth a second
-- purchase of the user's money.
--
-- The key is supplied by the caller and scoped to the account, so one client's
-- key can never collide with another's or be used to read someone else's report.
-- Replaying a key returns the report the first call produced: no vendor call, no
-- second charge, same answer.
ALTER TABLE credit_analytics_requests
    ADD COLUMN IF NOT EXISTS idempotency_key VARCHAR(64);

COMMENT ON COLUMN credit_analytics_requests.idempotency_key IS
    'Caller-supplied replay key, unique per account. NULL for calls that sent none (all rows predating this column).';

-- Partial, so the many existing rows with no key do not all collide on NULL —
-- and so a caller that sends no key keeps the old behaviour of every request
-- being distinct.
CREATE UNIQUE INDEX IF NOT EXISTS ux_credit_analytics_idempotency
    ON credit_analytics_requests (account_id, idempotency_key)
    WHERE idempotency_key IS NOT NULL;
