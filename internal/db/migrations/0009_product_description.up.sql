-- Products gain a customer-facing description: newline-separated feature lines the
-- clients render as a checklist on the plans screen. Copy lives here, not in code.
-- IF NOT EXISTS because this migration was numbered 0004 before main landed its
-- own 0004; a database that already applied the old numbering has the column and
-- must be able to replay this without failing.
ALTER TABLE products ADD COLUMN IF NOT EXISTS description TEXT NOT NULL DEFAULT '';

UPDATE products SET description = E'Experian credit score (live pull)\nFull credit report — all accounts & enquiries\nPersonalised improvement plan\nLoan & credit card eligibility\nPDF via email + WhatsApp'
WHERE code = 'CREDIT_ANALYSIS';

UPDATE products SET description = E'Income, spend & EMI breakdown\nTamper & authenticity check\nFOIR — your loan readiness\nAnalysis PDF to download'
WHERE code = 'BANK_STATEMENT_ANALYSIS';

UPDATE products SET description = E'Works with GPay, PhonePe & Paytm exports\nCategory & merchant spend split\nPersonalised savings insights'
WHERE code = 'UPI_STATEMENT_ANALYSIS';
