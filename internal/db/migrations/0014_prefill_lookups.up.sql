-- Audit trail for PAN verification lookups against Digitap's Mobile to Prefill
-- API: one row per call, whatever the outcome.
--
-- Why keep it: a verification decision is something a user can dispute months
-- later ("I entered my own PAN and you rejected it"), and without the provider's
-- own answer beside the decision there is no way to tell a provider data gap
-- from our bug. It also makes a repeat call unnecessary when support is simply
-- re-reading what happened, which matters when every call is billed.
--
-- WARNING — this table holds third-party personal data. A 101 response carries
-- name, date of birth, PAN, and (when those options are enabled on the client
-- id) email, addresses, alternate numbers and employment details, for a person
-- who gave that data to a bureau rather than to us. Under DPDP it is subject to
-- purpose limitation and storage limitation:
--
--   * response_raw stores only the fields the service decodes, NOT the whole
--     upstream body, so an option enabled at Digitap later cannot silently start
--     depositing addresses and salary bands here.
--   * There is no retention policy yet. One is required before launch — see the
--     note in docs/pan-verification.md. Purging rows past that window should be
--     a scheduled job, not a manual chore.
--   * Never expose this table through an API. It is for support and audit.
CREATE TABLE prefill_lookups (
    id              BIGSERIAL PRIMARY KEY,
    account_id      BIGINT      NOT NULL REFERENCES accounts (id) ON DELETE CASCADE,

    -- Digitap's request_id: the handle their support needs to trace one call.
    request_id      VARCHAR(64),
    -- Our client_ref_num for the same call, so both sides' logs line up.
    client_ref      VARCHAR(45),
    result_code     INT,
    message         VARCHAR(255),

    -- Outcome as the service judged it, so the decision is readable without
    -- re-running the comparison against a response format that may have moved on.
    pan_matched     BOOLEAN,
    name_matched    BOOLEAN,
    verified        BOOLEAN     NOT NULL DEFAULT false,
    provider_gap    BOOLEAN     NOT NULL DEFAULT false,

    -- The decoded subset of the provider's answer (name, dob, pan, documents).
    response_raw    JSONB,

    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_prefill_lookups_account ON prefill_lookups (account_id, created_at DESC);
CREATE INDEX idx_prefill_lookups_request ON prefill_lookups (request_id);
