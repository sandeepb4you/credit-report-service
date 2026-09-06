-- Reverse 0026. BANK_ACCOUNT rows cannot survive the narrower CHECK, so they go
-- first; nothing references bank_offerings, so this is a plain delete.
DELETE FROM bank_offerings WHERE product_type = 'BANK_ACCOUNT';

ALTER TABLE bank_offerings DROP CONSTRAINT IF EXISTS bank_offerings_type_chk;
ALTER TABLE bank_offerings
    ADD CONSTRAINT bank_offerings_type_chk
    CHECK (product_type IN ('FD_CARD', 'SECURED_LOAN'));

COMMENT ON COLUMN bank_offerings.product_type IS NULL;
