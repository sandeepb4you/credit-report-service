-- ---------------------------------------------------------------------------
-- admin_audience_exports: the audit trail of retargeting-list downloads.
--
-- The console's retargeting lists (Admin -> Retargeting) export customers'
-- names, phone numbers and email addresses for a WhatsApp, SMS, email or push
-- campaign. That is contact data leaving the system in bulk, so every file
-- generated is recorded here: who made it, from which list, for which channel,
-- how many accounts were selected and how many rows the file held.
--
-- Ids and counts only, like admin_data_exports and admin_impersonations: the
-- audit of an export must not become a second copy of what was exported.
-- ---------------------------------------------------------------------------
CREATE TABLE admin_audience_exports (
    id             BIGSERIAL   PRIMARY KEY,
    account_id     BIGINT      NOT NULL REFERENCES accounts (id),
    list_id        VARCHAR(8)  NOT NULL,
    channel        VARCHAR(16) NOT NULL,
    selected_count INTEGER     NOT NULL CHECK (selected_count >= 0),
    row_count      INTEGER     NOT NULL CHECK (row_count >= 0),
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX admin_audience_exports_account_idx ON admin_audience_exports (account_id, created_at DESC);
