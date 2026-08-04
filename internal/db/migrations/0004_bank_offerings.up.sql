-- ---------------------------------------------------------------------------
-- Bank offerings for the score-builder toolkit (Journey 05·C, S28).
--
-- An admin curates partner products that help a user rebuild credit —
-- principally the FD-secured credit card, the hero of the low-score toolkit.
-- Each row is one product from one bank, gated to a credit-score band, with an
-- estimated point impact (always paired with the compliance disclaimer), the
-- apply destination, and a revenue note for ops (referral/commission). The
-- score-builder service surfaces the offerings whose band contains the user's
-- current score, replacing the generic, hardcoded FD-card advice.
--
-- Mirrors loan_providers (migration 0003): admin CRUD, active flag, score-band
-- gating, and a dedicated RBAC permission ('bank-offering:manage').
-- ---------------------------------------------------------------------------
CREATE TABLE bank_offerings (
    id                     BIGSERIAL     PRIMARY KEY,

    name                   VARCHAR(120)  NOT NULL,          -- product / bank name
    product_type           VARCHAR(24)   NOT NULL,          -- FD_CARD | SECURED_LOAN

    -- The fixed deposit the user opens to obtain the card (0 when N/A). Shown
    -- on the hero card ("Open a ₹15,000 FD → ...").
    min_fd_amount          NUMERIC(12,2) NOT NULL DEFAULT 0,

    -- Annual rate for display only: the yield on the FD the user keeps earning
    -- while the card builds history (e.g. 7.000). Not a borrowing rate.
    interest_rate_percent  NUMERIC(6,3)  NOT NULL DEFAULT 0,

    -- This product targets scores in [min_credit_score, max_credit_score]. The
    -- score-builder surfaces it only when the user's score falls in the band.
    -- Defaults 0..900 = "everyone"; narrow it to aim a product at the rebuild band.
    min_credit_score       INTEGER       NOT NULL DEFAULT 0,
    max_credit_score       INTEGER       NOT NULL DEFAULT 900,

    -- Estimated score impact of taking up the product. Compliance: always
    -- rendered with the "estimated, not guaranteed" disclaimer.
    estimated_points_min   INTEGER       NOT NULL DEFAULT 0,
    estimated_points_max   INTEGER       NOT NULL DEFAULT 0,

    -- The CTA destination (deep link / landing page for the application).
    apply_url              TEXT          NOT NULL,

    -- Ops-facing referral/commission note (from the design's revenue tag).
    revenue_note           VARCHAR(120)  NOT NULL DEFAULT '',

    active                 BOOLEAN       NOT NULL DEFAULT TRUE,

    created_at             TIMESTAMPTZ   NOT NULL DEFAULT now(),
    updated_at             TIMESTAMPTZ   NOT NULL DEFAULT now(),

    CONSTRAINT bank_offerings_type_chk    CHECK (product_type IN ('FD_CARD','SECURED_LOAN')),
    CONSTRAINT bank_offerings_rate_chk    CHECK (interest_rate_percent >= 0 AND interest_rate_percent <= 100),
    CONSTRAINT bank_offerings_fd_chk      CHECK (min_fd_amount >= 0),
    CONSTRAINT bank_offerings_scoremin_chk CHECK (min_credit_score >= 0 AND min_credit_score <= 900),
    CONSTRAINT bank_offerings_scoremax_chk CHECK (max_credit_score >= 0 AND max_credit_score <= 900),
    CONSTRAINT bank_offerings_scoreband_chk CHECK (max_credit_score >= min_credit_score),
    CONSTRAINT bank_offerings_pts_chk     CHECK (estimated_points_min >= 0 AND estimated_points_max >= estimated_points_min)
);

-- Score-builder lookup is "active products whose band contains this score".
CREATE INDEX idx_bank_offerings_active_score ON bank_offerings (active, product_type, min_credit_score, max_credit_score);
