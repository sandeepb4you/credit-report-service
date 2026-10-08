-- ---------------------------------------------------------------------------
-- admin_impersonations: the audit trail of "View as user".
--
-- An admin can open a read-only view of a customer's app from the console.
-- Each view is one row: who looked, at whom, when it started, when its token
-- stops working, and when (if ever) the admin ended it early. The token carries
-- the row's id, and every request made with it is refused once ended_at is set
-- or expires_at has passed — so ending a view is a real revocation, not just
-- the app forgetting the token.
--
-- Ids only: nothing personal is copied here, so an account deletion has
-- nothing to scrub (the accounts row survives as a tombstone either way).
-- ---------------------------------------------------------------------------
CREATE TABLE admin_impersonations (
    id                BIGSERIAL   PRIMARY KEY,
    admin_account_id  BIGINT      NOT NULL REFERENCES accounts (id),
    target_account_id BIGINT      NOT NULL REFERENCES accounts (id),
    started_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at        TIMESTAMPTZ NOT NULL,
    ended_at          TIMESTAMPTZ,
    CHECK (admin_account_id <> target_account_id),
    CHECK (expires_at > started_at)
);

CREATE INDEX admin_impersonations_target_idx ON admin_impersonations (target_account_id, started_at DESC);
CREATE INDEX admin_impersonations_admin_idx  ON admin_impersonations (admin_account_id, started_at DESC);
