-- Credit analytics requests proxied to the Digitap /credit_analytics/request
-- API (Digitap Credit Analytics API Doc & Integration Guide V2.7, section 1.4.1).
--
-- One row per outbound call. The Experian/bureau payload is large and varies
-- between releases, so the request and the full upstream response are kept as
-- JSONB blobs rather than being normalized; key metadata (result_code, request_id,
-- http status) is lifted into columns for cheap indexing/filtering.

CREATE TABLE credit_analytics_requests (
    id              BIGSERIAL PRIMARY KEY,
    account_id      BIGINT      REFERENCES accounts (id) ON DELETE CASCADE,

    client_ref_num  VARCHAR(45) NOT NULL,             -- caller's correlation id
    mobile_no       VARCHAR(20) NOT NULL,             -- subject mobile number

    -- Fields lifted from the Digitap response envelope for filtering/reporting.
    request_id      VARCHAR(100),                     -- Digitap-assigned request id
    result_code     INTEGER,                          -- 101/102/103, NULL on error
    http_status     INTEGER,                          -- Digitap http_response_code
    message         TEXT,                             -- Digitap message

    request_body    JSONB       NOT NULL,             -- exact payload sent upstream
    response_body   JSONB,                            -- full Digitap response

    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_credit_analytics_account ON credit_analytics_requests (account_id);
CREATE INDEX idx_credit_analytics_created ON credit_analytics_requests (created_at);
