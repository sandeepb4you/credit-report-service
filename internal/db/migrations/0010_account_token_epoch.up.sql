-- ---------------------------------------------------------------------------
-- Make a role change take effect without waiting out the access token.
--
-- The role rides in the access JWT and is only re-read when a new one is
-- minted, so with a long auth.access-ttl a promoted or demoted account keeps
-- its old permissions for the life of its current token. This counter is
-- stamped into the token and bumped whenever the role changes: a token whose
-- stamp is stale is rejected with 401, which is the signal clients already
-- treat as "refresh and retry" — so the fix costs the user a refresh, not a
-- re-login.
--
-- Checked only on the permission-gated routes, so this adds no read to the
-- hot paths. See middleware.RequirePermission.
-- ---------------------------------------------------------------------------
ALTER TABLE accounts
    ADD COLUMN IF NOT EXISTS token_epoch INTEGER NOT NULL DEFAULT 0;
