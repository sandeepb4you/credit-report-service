-- ---------------------------------------------------------------------------
-- User-initiated account deletion: the grant that proves the OTP, and the
-- request that schedules the purge.
--
-- Google Play requires an app that creates accounts to let a user delete one
-- from inside the app AND from a public web page, without installing anything.
-- The web page at https://myscorr.com/delete-account is that second route, and
-- it is anonymous by necessity — somebody who has uninstalled the app still has
-- to be able to reach it. The OTP is therefore the only thing standing between
-- a typed phone number and somebody else's account, which is why this flow
-- mirrors password reset exactly rather than inventing a shorter one.
--
-- TWO TABLES, because the flow has two distinct secrets:
--
--   account_deletion_tokens   the single-use grant handed out when the code
--                             checks out, redeemed by the confirm step. The
--                             sibling of password_reset_tokens and
--                             signup_tokens; same 32-random-byte value, same
--                             SHA-256-only storage, same compare-and-set on
--                             consumed_at so two concurrent redemptions cannot
--                             both schedule a deletion.
--
--   account_deletion_requests the scheduled purge itself. Deliberately a row
--                             rather than a flag on accounts: "when was this
--                             asked for, through which channel, and was it
--                             cancelled" is the record a DPDP request is
--                             answered from, and a boolean column keeps none
--                             of it.
--
-- WHY A GRACE PERIOD RATHER THAN AN IMMEDIATE PURGE. The OTP is a single
-- factor sent to a device that may be lost, resold or SIM-swapped, and what it
-- authorises here is irreversible destruction of reports the user paid for.
-- Fourteen days of "sign in to cancel" turns a stolen code from a catastrophe
-- into a notification the real owner can act on. Play accepts a request-and-
-- wait flow provided the page says so, and the page does.
-- ---------------------------------------------------------------------------

CREATE TABLE account_deletion_tokens (
    id          BIGSERIAL   PRIMARY KEY,
    account_id  BIGINT      NOT NULL REFERENCES accounts (id) ON DELETE CASCADE,

    token_hash  CHAR(64)    NOT NULL UNIQUE,
    expires_at  TIMESTAMPTZ NOT NULL,
    consumed_at TIMESTAMPTZ,

    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Issuing a new grant invalidates the account's outstanding ones, which scans
-- by account_id over the live rows only.
CREATE INDEX idx_account_deletion_token_account ON account_deletion_tokens (account_id)
    WHERE consumed_at IS NULL;

CREATE TABLE account_deletion_requests (
    id           BIGSERIAL   PRIMARY KEY,
    account_id   BIGINT      NOT NULL REFERENCES accounts (id) ON DELETE CASCADE,

    -- PENDING   -> waiting out the grace period
    -- CANCELLED -> the account holder signed in, or asked support to stop it
    -- COMPLETED -> the sweep has purged it; the row survives as the receipt
    status       VARCHAR(16) NOT NULL DEFAULT 'PENDING'
                 CHECK (status IN ('PENDING', 'CANCELLED', 'COMPLETED')),

    -- How the request was proven, for the audit trail. The destination is
    -- stored MASKED (+91 98xxx xx041 / t****a@gmail.com) on purpose: this row
    -- outlives the purge as the receipt, and a receipt that keeps a full phone
    -- number would re-create the personal data the purge just destroyed.
    channel             VARCHAR(10) NOT NULL,
    destination_masked  VARCHAR(255) NOT NULL,

    requested_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- When the sweep may act. Stored rather than derived from requested_at so
    -- that changing the grace period later cannot retroactively move a window
    -- the user was already told about.
    scheduled_for TIMESTAMPTZ NOT NULL,

    cancelled_at     TIMESTAMPTZ,
    cancelled_reason VARCHAR(64),
    completed_at     TIMESTAMPTZ
);

-- At most one live request per account. A second confirm while one is already
-- pending is therefore a no-op that returns the existing date, rather than a
-- way to walk the deadline forward or backward.
CREATE UNIQUE INDEX idx_account_deletion_request_pending
    ON account_deletion_requests (account_id)
    WHERE status = 'PENDING';

-- The sweep's predicate: everything owed as of now.
CREATE INDEX idx_account_deletion_request_due
    ON account_deletion_requests (scheduled_for)
    WHERE status = 'PENDING';

-- The tombstone marker. accounts.status already documents 'DELETED' as part of
-- its lifecycle (see 0001) and nothing had ever set it; the purge does now.
-- deleted_at is separate because status alone cannot say when, and "when" is
-- what a retention audit asks for.
--
-- The accounts ROW SURVIVES the purge, stripped of every personal column. That
-- is not laziness — orders.account_id is NOT NULL REFERENCES accounts(id), and
-- the financial record has to outlive the person under Indian bookkeeping
-- rules. Keeping an anonymous shell is what lets both be true at once: the
-- money trail stays joinable, and it points at nobody.
ALTER TABLE accounts ADD COLUMN deleted_at TIMESTAMPTZ;
