-- ---------------------------------------------------------------------------
-- Loan providers + switch settings.
--
-- An admin curates, per loan type (home / personal / car), what each lender is
-- offering: the interest rate and the fees to move to them. The interest
-- optimizer (Journey 05·B, S24) uses these rows to compare a user's existing
-- loan against the market and estimate the savings of a balance transfer.
--
-- loan_switch_settings is a single-row config table so the recovery window
-- (how fast the switching cost must be recouped for the switch to be
-- recommended) and the default foreclosure fees can be tuned without a deploy.
-- ---------------------------------------------------------------------------
CREATE TABLE loan_providers (
    id                      BIGSERIAL     PRIMARY KEY,

    name                    VARCHAR(120)  NOT NULL,          -- lender / product name
    loan_type               VARCHAR(16)   NOT NULL,          -- HOME | PERSONAL | CAR

    -- Annual interest rate this provider offers switchers, e.g. 7.200 (%).
    interest_rate_percent   NUMERIC(6,3)  NOT NULL,

    -- Cost to move TO this provider: a percentage of the transferred balance
    -- plus an optional flat charge (either may be zero).
    processing_fee_percent  NUMERIC(6,3)  NOT NULL DEFAULT 0,
    processing_fee_flat     NUMERIC(12,2) NOT NULL DEFAULT 0,

    -- Offer is available only to scores at or above this (0 = everyone). Mirrors
    -- the "best rate for 800+ scores" banding on the S24 screen.
    min_credit_score        INTEGER       NOT NULL DEFAULT 0,

    -- Optional cap on the tenure this provider will refinance (NULL = no cap).
    max_tenure_months       INTEGER,

    active                  BOOLEAN       NOT NULL DEFAULT TRUE,

    created_at              TIMESTAMPTZ   NOT NULL DEFAULT now(),
    updated_at              TIMESTAMPTZ   NOT NULL DEFAULT now(),

    CONSTRAINT loan_providers_loan_type_chk CHECK (loan_type IN ('HOME','PERSONAL','CAR')),
    CONSTRAINT loan_providers_rate_chk      CHECK (interest_rate_percent >= 0 AND interest_rate_percent <= 100),
    CONSTRAINT loan_providers_procpct_chk   CHECK (processing_fee_percent >= 0 AND processing_fee_percent <= 100),
    CONSTRAINT loan_providers_procflat_chk  CHECK (processing_fee_flat >= 0),
    CONSTRAINT loan_providers_score_chk     CHECK (min_credit_score >= 0 AND min_credit_score <= 900),
    CONSTRAINT loan_providers_tenure_chk    CHECK (max_tenure_months IS NULL OR max_tenure_months > 0)
);

-- Optimizer lookup is "active offers for this loan type", so index that pair.
CREATE INDEX idx_loan_providers_type_active ON loan_providers (loan_type, active);

-- Single-row config. A CHECK pins the id so there is exactly one settings row;
-- the seed inserts it and every other column falls back to its default.
CREATE TABLE loan_switch_settings (
    id                                SMALLINT      PRIMARY KEY DEFAULT 1,

    -- The switch is recommended only if its total cost (current loan's
    -- foreclosure fee + new provider's processing fee) is recovered by the
    -- monthly EMI saving within this many months. 12 = "within a year".
    recovery_window_months            INTEGER       NOT NULL DEFAULT 12,

    -- Default foreclosure/pre-closure fee (% of outstanding) charged to exit the
    -- current loan, by type. The bureau report does not carry these, so they are
    -- configurable estimates. HOME defaults to 0: RBI bars foreclosure charges on
    -- floating-rate home loans to individuals.
    foreclosure_fee_percent_home      NUMERIC(6,3)  NOT NULL DEFAULT 0,
    foreclosure_fee_percent_personal  NUMERIC(6,3)  NOT NULL DEFAULT 4,
    foreclosure_fee_percent_car       NUMERIC(6,3)  NOT NULL DEFAULT 4,

    updated_at                        TIMESTAMPTZ   NOT NULL DEFAULT now(),

    CONSTRAINT loan_switch_settings_singleton CHECK (id = 1),
    CONSTRAINT loan_switch_settings_window_chk CHECK (recovery_window_months > 0)
);

INSERT INTO loan_switch_settings (id) VALUES (1) ON CONFLICT (id) DO NOTHING;
