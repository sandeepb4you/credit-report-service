-- Referral earnings with manual (admin-paid) withdrawals.
--
-- Attribution already exists (accounts.referred_by_account_id, set once at
-- signup). This adds the money on top of it:
--
-- 1. referral_earnings — one row per referred account, written once when that
--    account's FIRST order turns PAID (any product). UNIQUE on
--    referred_account_id is what makes the ₹125 a one-time credit rather than
--    one per purchase; a second paid order inserts nothing.
-- 2. payout_bank_accounts — one row per user: where withdrawals go. Upserted
--    from the app's "Change bank account" sheet. The full account number is
--    stored because the payout itself is manual: an admin reads it off the
--    withdrawal queue and pays from the business account. Treat like PAN —
--    PII, never logged, only returned to its owner (masked) and to the
--    withdrawal reviewer.
-- 3. referral_withdrawals — one row per withdraw request. PENDING on creation
--    (locking that amount out of the available balance), PAID once an admin
--    has paid it manually, REJECTED with a reason. The bank columns snapshot
--    the payout account at request time, so changing the account later cannot
--    rewrite where a pending request is headed.
--
-- Money math (in the service, pinned by tests): available = earned − paid −
-- pending. A request locks its amount; paying keeps it out; rejecting frees
-- it. Deducting only on pay would let two pending requests spend the same
-- rupees twice.
CREATE TABLE referral_earnings (
    id                   BIGSERIAL PRIMARY KEY,
    referrer_account_id  BIGINT      NOT NULL REFERENCES accounts (id) ON DELETE CASCADE,
    referred_account_id  BIGINT      NOT NULL UNIQUE REFERENCES accounts (id) ON DELETE CASCADE,
    -- Which paid order triggered the credit, for support follow-up.
    order_uid            TEXT        NOT NULL,
    amount_paise         INTEGER     NOT NULL CHECK (amount_paise > 0),
    credited_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- A referrer cannot earn off themselves; the signup path already guards
    -- this, this is the backstop.
    CONSTRAINT referral_earnings_no_self_credit CHECK (referrer_account_id <> referred_account_id)
);

CREATE INDEX idx_referral_earnings_referrer
    ON referral_earnings (referrer_account_id, credited_at DESC);

CREATE TABLE payout_bank_accounts (
    account_id     BIGINT      PRIMARY KEY REFERENCES accounts (id) ON DELETE CASCADE,
    holder_name    TEXT        NOT NULL,
    -- Full number, for the manual payout. Display only ever shows the last 4.
    account_number TEXT        NOT NULL,
    ifsc           VARCHAR(11) NOT NULL,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE referral_withdrawals (
    id                    BIGSERIAL PRIMARY KEY,
    account_id            BIGINT      NOT NULL REFERENCES accounts (id) ON DELETE CASCADE,
    amount_paise          INTEGER     NOT NULL CHECK (amount_paise > 0),
    status                VARCHAR(16) NOT NULL DEFAULT 'PENDING'
        CHECK (status IN ('PENDING', 'PAID', 'REJECTED')),
    -- Bank snapshot at request time (see header).
    holder_name           TEXT        NOT NULL,
    account_last4         CHAR(4)     NOT NULL,
    ifsc                  VARCHAR(11) NOT NULL,
    requested_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    decided_at            TIMESTAMPTZ,
    decided_by_account_id BIGINT REFERENCES accounts (id) ON DELETE SET NULL,
    reject_reason         TEXT
);

CREATE INDEX idx_referral_withdrawals_account
    ON referral_withdrawals (account_id, requested_at DESC);
CREATE INDEX idx_referral_withdrawals_status
    ON referral_withdrawals (status, requested_at DESC);
