-- Bank-account offerings for the Explore grid (design/onboarding/11.html).
--
-- The unlocked Home shows, per Explore tile, how many curated products target
-- the user's score. Credit cards are already the FD_CARD rows in this table;
-- bank accounts had nowhere to live. Rather than a second catalog with the same
-- shape (name, score band, apply link, active flag, admin CRUD, the same RBAC
-- permission), a BANK_ACCOUNT row lives here with the FD- and points-specific
-- columns left at their zero defaults — an account is not a credit product and
-- moves no score, so there is nothing to put in them.
--
-- A widened CHECK, not a dropped one: product_type is what the Explore endpoint
-- groups by, so a typo'd type would silently vanish from every tile.
ALTER TABLE bank_offerings DROP CONSTRAINT IF EXISTS bank_offerings_type_chk;
ALTER TABLE bank_offerings
    ADD CONSTRAINT bank_offerings_type_chk
    CHECK (product_type IN ('FD_CARD', 'SECURED_LOAN', 'BANK_ACCOUNT'));

COMMENT ON COLUMN bank_offerings.product_type IS
    'FD_CARD = FD-secured credit card (score builder + Explore "Credit Cards"); '
    'SECURED_LOAN = reserved; BANK_ACCOUNT = savings/current account for Explore "Bank Accounts".';
