-- Reverse 0025: drop the presentation columns and put the Monthly checklist back.
UPDATE products SET
    description = E'Everything in Quarterly\nScore refresh every month\nFull score history & trend graph\nCheapest per refresh',
    updated_at  = now()
 WHERE code = 'SCORE_PLUS_MONTHLY';

ALTER TABLE products
    DROP COLUMN IF EXISTS badge,
    DROP COLUMN IF EXISTS tagline,
    DROP COLUMN IF EXISTS sort_order;
