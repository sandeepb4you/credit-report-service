-- Presentation fields for the plans screen (design/onboarding/09.html), so the
-- card copy an operator wants to tune — the badge, the one-line tagline, the
-- order the plans appear in — lives in the catalog rather than in the app.
--
-- Everything the design shows that is NOT here is arithmetic the client derives
-- from fields that already exist: per-refresh price = amount / checks_included,
-- the struck-through "list" price = one-time price × checks_included, the
-- saving = the difference, the "% OFF" = saving / list. Storing those too would
-- let a price change leave a stale saving on screen.
--
-- badge and tagline are nullable: absent means "draw no badge / no tagline",
-- not an empty pill. sort_order defaults high so an unranked product lands
-- last rather than first.
ALTER TABLE products
    ADD COLUMN IF NOT EXISTS badge      TEXT,
    ADD COLUMN IF NOT EXISTS tagline    TEXT,
    ADD COLUMN IF NOT EXISTS sort_order INT NOT NULL DEFAULT 100;

COMMENT ON COLUMN products.badge IS
    'Short pill above the plan name on the plans screen, e.g. "★ MOST POPULAR". NULL = no pill.';
COMMENT ON COLUMN products.tagline IS
    'One line under the plan name, e.g. "Refreshed every month, without fail". NULL = none.';
COMMENT ON COLUMN products.sort_order IS
    'Position on the plans screen, ascending. The first product is drawn as the featured card.';

-- 09.html: Monthly leads with full detail, Quarterly and Starter follow condensed.
UPDATE products SET badge = '★ MOST POPULAR', tagline = 'Refreshed every month, without fail',
                    sort_order = 10, updated_at = now()
 WHERE code = 'SCORE_PLUS_MONTHLY';
UPDATE products SET badge = '◆ BEST VALUE',   tagline = 'Refreshed every 3 months',
                    sort_order = 20, updated_at = now()
 WHERE code = 'SCORE_PLUS_QUARTERLY';
UPDATE products SET badge = '● TRENDING',     tagline = 'One-time · pay once, whenever you need it',
                    sort_order = 30, updated_at = now()
 WHERE code = 'CREDIT_ANALYSIS';

-- The Monthly checklist as 09 words it. The old first line ("Everything in
-- Quarterly") made the featured card depend on reading a card below it.
UPDATE products SET
    description = E'Refresh every month\nInstant WhatsApp change alerts\nFull score history & trend graph\nLoan & credit card eligibility',
    updated_at  = now()
 WHERE code = 'SCORE_PLUS_MONTHLY';
