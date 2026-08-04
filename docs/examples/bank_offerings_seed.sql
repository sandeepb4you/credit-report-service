-- ---------------------------------------------------------------------------
-- Sample bank offerings for the score-builder toolkit (Journey 05·C, S28).
--
-- These power the hero card on the low-score journey: an FD-secured credit
-- card with a real bank name, an apply link, and an estimated point impact.
-- The score-builder surfaces the offerings whose [min_credit_score,
-- max_credit_score] band contains the user's current score, so the 610 persona
-- sees the rebuild-band products while an 815 sees none (that journey is
-- "protect", not "rebuild").
--
-- The migrations create these tables in the `report` schema (the DB URL sets
-- search_path=report). Set it here too so the plain table names resolve when
-- you run this by hand via psql.
-- ---------------------------------------------------------------------------
SET search_path TO report;

BEGIN;

-- Re-runnable: clear any previously seeded rows. Safe on a test DB only —
-- nothing references bank_offerings, so this just resets the catalog.
TRUNCATE TABLE bank_offerings RESTART IDENTITY;

-- name, product_type, min_fd_amount, interest_rate_percent,
-- min_credit_score, max_credit_score, estimated_points_min, estimated_points_max,
-- apply_url, revenue_note, active
INSERT INTO bank_offerings
    (name, product_type, min_fd_amount, interest_rate_percent,
     min_credit_score, max_credit_score, estimated_points_min, estimated_points_max,
     apply_url, revenue_note, active)
VALUES
    -- Rebuild band (low scores). The hero options the 610 persona sees.
    ('Axis Bank Insta Easy Card',     'FD_CARD', 15000, 7.000,   0, 650,  40, 80,
     'https://apply.axisbank.co.in/insta-easy',  'FD + secured-card referral', TRUE),
    ('ICICI Bank Coral secured card', 'FD_CARD', 20000, 7.100,   0, 680,  45, 85,
     'https://apply.icicibank.com/coral-secured', 'FD + secured-card referral', TRUE),
    ('SBI Simply Save secured',       'FD_CARD', 15000, 6.850, 550, 650,  40, 75,
     'https://apply.sbicard.com/simply-save-secured', 'FD + secured-card referral', TRUE),
    ('IDFC FIRST WOW Card',           'FD_CARD', 10000, 7.000,   0, 700,  40, 80,
     'https://apply.idfcfirstbank.com/wow', 'FD + secured-card referral', TRUE),

    -- Blended band (650–749): only surfaces when the user graduates out of
    -- the rebuild band but still benefits from a secured card.
    ('HDFC Millennia FD-Backed',      'FD_CARD', 25000, 7.000, 650, 749,  30, 60,
     'https://apply.hdfcbank.com/millennia-fd', 'FD + secured-card referral', TRUE),

    -- Higher-band product that should NOT surface for the 610 persona (band
    -- starts at 700). Useful to confirm band filtering.
    ('Axis Bank Reserve secured',     'FD_CARD', 200000, 7.000, 700, 900, 15, 30,
     'https://apply.axisbank.co.in/reserve-secured', 'FD + secured-card referral', TRUE),

    -- Inactive — must be ignored by the score-builder.
    ('Old Product (inactive)',        'FD_CARD', 10000, 6.500,   0, 650,  40, 80,
     'https://apply.example.com/old', 'FD + secured-card referral', FALSE);

COMMIT;

-- Quick check (the 610 persona should see the first four active, rebuild-band
-- offerings; the HDFC 650+ and Axis 700+ ones stay out):
--
--   SELECT name, min_credit_score, max_credit_score, estimated_points_max, active
--     FROM bank_offerings
--    WHERE active AND product_type = 'FD_CARD'
--      AND min_credit_score <= 610 AND max_credit_score >= 610
--    ORDER BY estimated_points_max DESC;
