-- ---------------------------------------------------------------------------
-- Loan Assist: business-loan expert help, ₹2,999 per application (design:
-- scorrclub/design/myscorr-loan-assist-customer-screens.html, 01–19).
--
-- A customer opens an application for a loan type and an amount, adds the
-- documents that loan type needs (or reuses ones a partner already accepted),
-- agrees to share them and pays. A partner — the offline executive or the
-- online support team — is assigned, reviews each document (accepting it or
-- asking for it again), moves the application through its stages, and calls.
-- Nothing here grants a score check.
-- ---------------------------------------------------------------------------

-- products.kind separates the score catalog (what the Plans screen lists, what
-- a coupon with no product scope applies to, what "one-time check" pricing is
-- derived from) from services sold through their own flow. Every existing row
-- is a score product, so the default keeps them meaning exactly what they did.
ALTER TABLE products
    ADD COLUMN IF NOT EXISTS kind VARCHAR(16) NOT NULL DEFAULT 'SCORE';

ALTER TABLE products
    ADD CONSTRAINT products_kind_check CHECK (kind IN ('SCORE', 'SERVICE'));

COMMENT ON COLUMN products.kind IS
    'SCORE = score checks and plans (the Plans catalog); SERVICE = sold through its own flow, never listed on GET /products.';

-- checks_included 0: the purchase buys no score check, so nothing that counts
-- checks (the invoice chips, the plan arithmetic) can read one into it.
INSERT INTO products (code, name, amount, currency, active, description,
                      checks_included, kind, sort_order)
VALUES ('LOAN_ASSIST', 'Loan Assist', 2999.00, 'INR', TRUE,
        E'Profile check by a loan expert\nExpert call to guide you to loans that fit your need',
        0, 'SERVICE', 1000)
ON CONFLICT (code) DO NOTHING;

-- One row per application. A DRAFT is the customer's open application while
-- they choose a loan type and add documents; at most one per account (the
-- partial unique index), so every upload lands on the same draft. Payment
-- turns it SUBMITTED and gives it the request code the customer quotes; the
-- assigned partner moves it on from there. "Action needed" is not a status:
-- it is any document of the application waiting to be uploaded again.
CREATE TABLE loan_assist_requests (
    id            BIGSERIAL PRIMARY KEY,
    account_id    BIGINT      NOT NULL REFERENCES accounts (id) ON DELETE CASCADE,
    status        VARCHAR(20) NOT NULL DEFAULT 'DRAFT'
                  CHECK (status IN ('DRAFT', 'SUBMITTED', 'DOCS_VERIFIED',
                                    'EXPERT_CONTACTED', 'GUIDANCE', 'CLOSED')),
    -- Which loan the customer needs help with (service.loanTypes), and how
    -- much, in whole rupees.
    loan_type     VARCHAR(32) NOT NULL DEFAULT 'BUSINESS',
    amount_needed BIGINT      NOT NULL DEFAULT 0 CHECK (amount_needed >= 0),
    -- "The premises is rented": makes the rent agreement a required document.
    rented        BOOLEAN     NOT NULL DEFAULT FALSE,
    -- The consent, stamped when the customer submits for payment, with the
    -- exact sentence they agreed to.
    consent_at    TIMESTAMPTZ,
    consent_text  TEXT,
    -- Public reference, LA-<n>. Minted at payment, not before.
    request_code  VARCHAR(16) UNIQUE,
    -- SET NULL so an account reset that deletes orders is never blocked here.
    order_id      BIGINT      REFERENCES orders (id) ON DELETE SET NULL,
    paid_at       TIMESTAMPTZ,
    -- The partner working the application. Only they (and admins) may open
    -- its documents: they include Aadhaar copies.
    assigned_partner_id BIGINT REFERENCES accounts (id) ON DELETE SET NULL,
    assigned_at   TIMESTAMPTZ,
    -- When each stage was reached, for the customer's progress steps.
    docs_verified_at    TIMESTAMPTZ,
    expert_contacted_at TIMESTAMPTZ,
    guidance_at         TIMESTAMPTZ,
    closed_at           TIMESTAMPTZ,
    -- The partners' own notes; never shown to the customer.
    notes         TEXT,
    team_notified_at TIMESTAMPTZ,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX uq_loan_assist_one_draft
    ON loan_assist_requests (account_id) WHERE status = 'DRAFT';
CREATE INDEX idx_loan_assist_account ON loan_assist_requests (account_id, created_at DESC);
CREATE INDEX idx_loan_assist_paid    ON loan_assist_requests (paid_at DESC) WHERE paid_at IS NOT NULL;
CREATE INDEX idx_loan_assist_partner ON loan_assist_requests (assigned_partner_id) WHERE status <> 'DRAFT';

-- Which stored document fills which slot of an application, and what its
-- partner made of it. The bytes are a documents row like the PAN card's.
--
-- One document may fill a slot in several applications: a document a partner
-- accepted is REUSED by a later application by linking it, not copying it
-- (reused_from_request_id says where it was accepted). A document is deleted
-- only when no slot refers to it any more.
CREATE TABLE loan_assist_documents (
    request_id   BIGINT      NOT NULL REFERENCES loan_assist_requests (id) ON DELETE CASCADE,
    slot         VARCHAR(32) NOT NULL,
    document_id  BIGINT      NOT NULL REFERENCES documents (id) ON DELETE CASCADE,
    -- PENDING until a partner looks; ACCEPTED, or RETAKE with a note asking
    -- the customer for it again. A new upload puts a RETAKE back to PENDING.
    review_state VARCHAR(10) NOT NULL DEFAULT 'PENDING'
                 CHECK (review_state IN ('PENDING', 'ACCEPTED', 'RETAKE')),
    review_note  TEXT,
    reviewed_by  BIGINT      REFERENCES accounts (id) ON DELETE SET NULL,
    reviewed_at  TIMESTAMPTZ,
    -- Set when the customer uploads it again after a RETAKE ("Sent again").
    resubmitted_at TIMESTAMPTZ,
    reused_from_request_id BIGINT REFERENCES loan_assist_requests (id) ON DELETE SET NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (request_id, slot)
);

CREATE INDEX idx_loan_assist_documents_doc ON loan_assist_documents (document_id);

-- How a partner appears to the customer they call ("Vikram Rao · Offline
-- Executive · Hyderabad") and the number the Call button dials — a myScorr
-- line, not necessarily the partner's own phone.
CREATE TABLE assist_partner_profiles (
    account_id   BIGINT       PRIMARY KEY REFERENCES accounts (id) ON DELETE CASCADE,
    display_name VARCHAR(80)  NOT NULL,
    team         VARCHAR(40)  NOT NULL DEFAULT 'Online Support',
    city         VARCHAR(60),
    call_phone   VARCHAR(20),
    updated_at   TIMESTAMPTZ  NOT NULL DEFAULT now()
);
