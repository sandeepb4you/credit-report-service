-- ---------------------------------------------------------------------------
-- Tax invoices: one per paid order, numbered, snapshotted, and kept.
--
-- An invoice is ISSUED once, at the moment an order first turns PAID, and is
-- never edited afterwards except by the account-deletion scrub (below). Every
-- figure on it is a column here rather than something re-derived at render
-- time, because the document has to say what was true when it was issued: a
-- later price change, catalog edit or GST-rate change must not rewrite an
-- invoice somebody has already been sent. The PDF is a rendering of this row
-- and can be regenerated from it; the row is the record.
--
-- NUMBERING. GST (CGST Rules, rule 46(b)) wants a consecutive serial, unique
-- within a financial year, at most 16 characters: MSC/26-27/000184. A Postgres
-- SEQUENCE cannot give that — a rolled-back transaction burns a value, and a
-- gap in the series is exactly what an auditor asks about. invoice_counters
-- holds one row per (series, financial year), incremented under the row lock
-- in the same transaction as the invoice insert, so a number exists if and
-- only if its invoice does.
--
-- Two series. 'MSC' is the tax-invoice series, for orders paid through the
-- live Cashfree environment. 'TST' is for sandbox orders (the internal builds'
-- test payments): no money moved, so what they get is a SPECIMEN, visibly not a
-- tax invoice, from a series of its own — a test purchase must never consume a
-- number from the real one.
--
-- RETENTION. An invoice outlives both of the things that can remove its order:
--   * account reset (an admin testing tool) deletes the order; order_id goes
--     NULL and the invoice stays, because deleting it would put a hole in the
--     series. order_uid is copied onto the row for the same reason.
--   * account deletion keeps orders anyway, and scrubs the invoice's billed-to
--     columns and deletes its PDF (repository.PurgeAccount), which is what keeps
--     the privacy policy's "kept with your name, number and email removed" true.
--     Nothing the rule-46 minimum needs for a B2C supply under ₹50,000 is in
--     those columns.
-- ---------------------------------------------------------------------------

CREATE TABLE invoice_counters (
    series          VARCHAR(8) NOT NULL,
    financial_year  CHAR(5)    NOT NULL,          -- '26-27'
    last_number     INTEGER    NOT NULL CHECK (last_number > 0),
    PRIMARY KEY (series, financial_year)
);

CREATE TABLE invoices (
    id                     BIGSERIAL PRIMARY KEY,
    invoice_number         VARCHAR(16)  NOT NULL UNIQUE,
    specimen               BOOLEAN      NOT NULL,
    order_id               BIGINT       UNIQUE REFERENCES orders (id) ON DELETE SET NULL,
    order_uid              VARCHAR(64)  NOT NULL,
    account_id             BIGINT       NOT NULL REFERENCES accounts (id),
    issued_at              TIMESTAMPTZ  NOT NULL,

    -- What was sold. valid_until is the plan's last day (scheduled checks
    -- expire when expires_on < today, so the day itself is still covered);
    -- NULL for a one-time check.
    product_code           VARCHAR(64)  NOT NULL,
    valid_until            DATE,

    -- Money, in paise. total = taxable + cgst + sgst + igst, always; the CHECK
    -- is the one arithmetic fact about an invoice worth refusing to store
    -- without. Prices are GST-inclusive, so taxable is derived from the total
    -- at issue and the tax is the remainder (see service.splitGST).
    currency               CHAR(3)      NOT NULL,
    total_paise            BIGINT       NOT NULL CHECK (total_paise >= 0),
    taxable_paise          BIGINT       NOT NULL CHECK (taxable_paise >= 0),
    cgst_paise             BIGINT       NOT NULL DEFAULT 0,
    sgst_paise             BIGINT       NOT NULL DEFAULT 0,
    igst_paise             BIGINT       NOT NULL DEFAULT 0,
    gst_rate_percent       NUMERIC(5,2) NOT NULL,
    list_price_paise       BIGINT       NOT NULL,
    discount_paise         BIGINT       NOT NULL DEFAULT 0,
    coupon_code            VARCHAR(64),
    CHECK (total_paise = taxable_paise + cgst_paise + sgst_paise + igst_paise),

    place_of_supply        VARCHAR(64)  NOT NULL,
    place_of_supply_code   CHAR(2)      NOT NULL,

    -- The supplier as configured at issue. A GSTIN is required for a tax
    -- invoice and absent on a specimen issued before one was configured.
    supplier_name          VARCHAR(255) NOT NULL,
    supplier_address       TEXT         NOT NULL,
    supplier_gstin         VARCHAR(15),
    sac                    VARCHAR(8),
    CHECK (specimen OR (supplier_gstin IS NOT NULL AND sac IS NOT NULL)),

    -- The recipient as the account read at issue. All three are scrubbed to
    -- NULL when the account is deleted.
    billed_to_name         VARCHAR(255),
    billed_to_email        VARCHAR(255),
    billed_to_phone        VARCHAR(32),

    -- Presentation: plan name, badge, tagline, checklist and chips, as the
    -- catalog read at issue. JSON because it is only ever rendered, never
    -- queried, and its shape follows the design rather than the ledger.
    details                JSONB        NOT NULL,

    -- How it was paid, from Cashfree's payment record. Filled at issue when the
    -- webhook or reconcile call carried it, otherwise on first render.
    payment_label          VARCHAR(80),
    payment_ref_label      VARCHAR(32),
    payment_ref            VARCHAR(64),
    payment_fetch_attempts INTEGER      NOT NULL DEFAULT 0,

    pdf_uri                TEXT,

    -- The automatic email on payment. PENDING only when the account had an
    -- address at issue; the delivery worker moves it to SENT, or to FAILED once
    -- its retries run out. NO_ADDRESS is final — adding an email later does not
    -- back-send, the user asks for it from My Purchases. CANCELLED is what a
    -- purge leaves on a mail that had not gone yet.
    auto_email             VARCHAR(16)  NOT NULL
        CHECK (auto_email IN ('PENDING', 'SENT', 'NO_ADDRESS', 'FAILED', 'CANCELLED')),
    auto_email_attempts    INTEGER      NOT NULL DEFAULT 0,
    next_attempt_at        TIMESTAMPTZ,
    last_error             TEXT,

    created_at             TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at             TIMESTAMPTZ  NOT NULL DEFAULT now()
);

CREATE INDEX invoices_account_idx ON invoices (account_id);
CREATE INDEX invoices_pending_email_idx ON invoices (next_attempt_at)
    WHERE auto_email = 'PENDING';

-- Every email an invoice went out in, for the rate limits: a 30-second
-- cooldown per invoice, and a daily cap on sends to an address the account
-- does not own (the "send once" path, which is otherwise a way to mail a
-- stranger). The recipient is stored as a digest only: this table exists to
-- count, and a list of addresses somebody typed is not worth keeping.
CREATE TABLE invoice_email_sends (
    id              BIGSERIAL   PRIMARY KEY,
    invoice_id      BIGINT      NOT NULL REFERENCES invoices (id) ON DELETE CASCADE,
    account_id      BIGINT      NOT NULL REFERENCES accounts (id) ON DELETE CASCADE,
    kind            VARCHAR(16) NOT NULL CHECK (kind IN ('AUTO', 'ACCOUNT', 'ONE_TIME')),
    recipient_hash  VARCHAR(64) NOT NULL,
    sent_at         TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX invoice_email_sends_invoice_idx ON invoice_email_sends (invoice_id, sent_at);
