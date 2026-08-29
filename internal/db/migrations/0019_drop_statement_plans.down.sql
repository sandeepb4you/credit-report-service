-- Restore the two plans, seeded exactly as 0001 and 0009 left them, and inactive
-- rather than on sale: coming back down a migration should not silently start
-- selling something again.
INSERT INTO products (code, name, amount, description, active) VALUES
    ('BANK_STATEMENT_ANALYSIS', 'Bank Statement Analysis', 299.00,
     E'Income, spend & EMI breakdown\nTamper & authenticity check\nFOIR — your loan readiness\nAnalysis PDF to download', false),
    ('UPI_STATEMENT_ANALYSIS', 'UPI Statement Analysis', 299.00,
     E'Works with GPay, PhonePe & Paytm exports\nCategory & merchant spend split\nPersonalised savings insights', false)
ON CONFLICT (code) DO NOTHING;
