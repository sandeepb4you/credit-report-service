TRUNCATE TABLE loan_providers RESTART IDENTITY;   -- re-runnable (test DB only)

INSERT INTO loan_providers
(name, loan_type, interest_rate_percent, processing_fee_percent, processing_fee_flat, min_credit_score, max_tenure_months, active)
VALUES
    -- HOME
    ('Kotak Mahindra Bank',   'HOME',  7.200, 0.000, 5000, 800, 360, TRUE),  -- => RECOMMENDED for the 815 home loan
    ('State Bank of India',   'HOME',  7.250, 0.250,    0, 750, 360, TRUE),
    ('ICICI Bank',            'HOME',  7.350, 0.500, 3000, 700, 300, TRUE),
    ('LIC Housing Finance',   'HOME',  7.500, 0.250,    0, 650, 360, TRUE),  -- >7.45 -> never beats sample
    ('Bajaj Housing Finance', 'HOME',  7.100, 0.000, 4999, 820, 360, TRUE),  -- cheapest BUT needs 820 -> filtered for 815
    ('Old Deal (inactive)',   'HOME',  6.990, 0.000,    0,   0, 360, FALSE), -- active=false -> ignored
    -- PERSONAL
    ('HDFC Bank',             'PERSONAL', 10.500, 1.000,    0, 800, 60, TRUE),
    ('IDFC FIRST Bank',       'PERSONAL', 11.490, 1.500,    0, 700, 60, TRUE),
    ('Axis Bank',             'PERSONAL', 11.900, 1.500, 1999, 720, 60, TRUE),
    ('Kotak Mahindra Bank',   'PERSONAL', 12.990, 2.000,    0, 650, 48, TRUE),
    ('Tata Capital',          'PERSONAL', 13.500, 2.500, 2500, 600, 48, TRUE),  -- qualifies for the 610 persona
    -- CAR
    ('HDFC Bank',             'CAR', 7.900, 0.500, 1500, 780, 84, TRUE),  -- cheapest for 815 auto (8.40 -> 7.90)
    ('Bank of Baroda',        'CAR', 8.150, 0.250, 1500, 720, 84, TRUE),
    ('Kotak Mahindra Bank',   'CAR', 8.100, 1.000, 2000, 750, 84, TRUE),  -- current lender -> excluded from its own switch
    ('State Bank of India',   'CAR', 8.250, 0.000, 1000, 700, 84, TRUE),
    ('ICICI Bank',            'CAR', 8.600, 0.500,    0, 650, 72, TRUE);  -- >8.40 -> never beats sample
COMMIT;