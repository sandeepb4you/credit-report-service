-- ---------------------------------------------------------------------------
-- Sample loan providers for testing the loan-switch / interest-optimizer flow.
--
-- The migrations create these tables in the `report` schema (the DB URL sets
-- search_path=report). Set it here too so the plain table names resolve when
-- you run this by hand via psql.
-- ---------------------------------------------------------------------------
SET search_path TO report;

BEGIN;

-- Re-runnable: clear any previously seeded rows. Safe on a test DB only —
-- nothing references loan_providers, so this just resets the catalog.
TRUNCATE TABLE loan_providers RESTART IDENTITY;

-- name, loan_type, interest_rate_percent, processing_fee_percent,
-- processing_fee_flat, min_credit_score, max_tenure_months, active
INSERT INTO loan_providers
    (name, loan_type, interest_rate_percent, processing_fee_percent, processing_fee_flat, min_credit_score, max_tenure_months, active)
VALUES
    -- ---- HOME ------------------------------------------------------------
    ('Kotak Mahindra Bank',   'HOME',  7.200, 0.000, 5000, 800, 360, TRUE),  -- cheapest for 800+; flat fee -> ~4-month recovery => RECOMMENDED
    ('State Bank of India',   'HOME',  7.250, 0.250,    0, 750, 360, TRUE),
    ('ICICI Bank',            'HOME',  7.350, 0.500, 3000, 700, 300, TRUE),
    ('LIC Housing Finance',   'HOME',  7.500, 0.250,    0, 650, 360, TRUE),  -- not below 7.45 -> never beats the sample 815 home loan
    ('Bajaj Housing Finance', 'HOME',  7.100, 0.000, 4999, 820, 360, TRUE),  -- cheapest overall BUT needs 820 -> filtered out for the 815 persona
    ('Old Deal (inactive)',   'HOME',  6.990, 0.000,    0,   0, 360, FALSE), -- active=false -> ignored by the optimizer

    -- ---- PERSONAL --------------------------------------------------------
    ('HDFC Bank',             'PERSONAL', 10.500, 1.000,    0, 800, 60, TRUE),
    ('IDFC FIRST Bank',       'PERSONAL', 11.490, 1.500,    0, 700, 60, TRUE),
    ('Axis Bank',             'PERSONAL', 11.900, 1.500, 1999, 720, 60, TRUE),
    ('Kotak Mahindra Bank',   'PERSONAL', 12.990, 2.000,    0, 650, 48, TRUE),
    ('Tata Capital',          'PERSONAL', 13.500, 2.500, 2500, 600, 48, TRUE),  -- qualifies for the 610 persona's 14% loan

    -- ---- CAR -------------------------------------------------------------
    ('HDFC Bank',             'CAR', 7.900, 0.500, 1500, 780, 84, TRUE),  -- cheapest for the 815 auto loan (8.40% -> 7.90%)
    ('Bank of Baroda',        'CAR', 8.150, 0.250, 1500, 720, 84, TRUE),
    ('Kotak Mahindra Bank',   'CAR', 8.100, 1.000, 2000, 750, 84, TRUE),  -- current lender on the sample auto loan -> excluded as a "switch"
    ('State Bank of India',   'CAR', 8.250, 0.000, 1000, 700, 84, TRUE),
    ('ICICI Bank',            'CAR', 8.600, 0.500,    0, 650, 72, TRUE);  -- above 8.40% -> never beats the sample auto loan

-- The switch settings (recovery window + default foreclosure fees) are already
-- seeded by migration 0003 with sane defaults:
--   recovery_window_months = 12, HOME foreclosure 0%, PERSONAL/CAR 4%.
-- Uncomment to tune them for testing. e.g. drop CAR foreclosure to 0 so the
-- sample auto-loan switch flips to "recommended", or widen the window:
--
-- UPDATE loan_switch_settings
--    SET recovery_window_months       = 12,
--        foreclosure_fee_percent_home = 0,
--        foreclosure_fee_percent_car  = 0,
--        updated_at                   = now()
--  WHERE id = 1;

COMMIT;

-- Quick check:
--   SELECT loan_type, name, interest_rate_percent, min_credit_score, active
--     FROM loan_providers ORDER BY loan_type, interest_rate_percent;
