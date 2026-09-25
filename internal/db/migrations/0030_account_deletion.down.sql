-- Reverses 0030. The accounts column goes last: dropping it while a request
-- row still referenced the account it marks would leave a tombstone nothing
-- identifies as one.
DROP TABLE IF EXISTS account_deletion_tokens;
DROP TABLE IF EXISTS account_deletion_requests;
ALTER TABLE accounts DROP COLUMN IF EXISTS deleted_at;
