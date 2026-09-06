-- Shorter plan names, and one claim the app cannot keep.
--
-- The name is what the user sees wherever a purchase is named — the plans
-- screen (design/onboarding/09.html) and now "My purchases", which used to
-- print the raw catalog code (CREDIT_ANALYSIS) because an order carries only
-- the code. A migration rather than a console edit because PATCH
-- /admin/plans/{code} covers badge, tagline, description and sort order but
-- not the name, so this is the only place a rename can live and still reach
-- every environment.
--
-- The Quarterly checklist names the one-time product in its first line, so it
-- is rewritten in the same statement — a checklist pointing at "myScorr
-- Starter Plan" would name a plan the screen beside it no longer sells.
UPDATE products SET name = 'myScorr Starter', updated_at = now()
 WHERE code = 'CREDIT_ANALYSIS';

UPDATE products SET name        = 'myScorr Quarterly',
                    description = replace(description, 'myScorr Starter Plan', 'myScorr Starter'),
                    updated_at  = now()
 WHERE code = 'SCORE_PLUS_QUARTERLY';

UPDATE products SET name = 'myScorr Monthly', updated_at = now()
 WHERE code = 'SCORE_PLUS_MONTHLY';

-- Drop the "Instant WhatsApp change alerts" line from the Monthly checklist.
-- Nothing sends a WhatsApp message today: report delivery shows the WhatsApp
-- option disabled for exactly that reason (no provider is wired), so the plan
-- card was the one place in the app still promising it.
--
-- chr(10) rather than an E'' escape string, and two plain replaces rather than
-- one regexp_replace with a backreference: the checklist is newline-separated,
-- so the line has to take its own newline with it whether it sits in the middle
-- (first replace) or last (second), and neither form here has a backslash to
-- get mangled on the way into the file. Deliberately not a rewritten
-- description, so an operator's edits to the other lines survive this
-- migration.
UPDATE products
   SET description = replace(
                       replace(description, 'Instant WhatsApp change alerts' || chr(10), ''),
                       chr(10) || 'Instant WhatsApp change alerts', ''),
       updated_at  = now()
 WHERE code = 'SCORE_PLUS_MONTHLY';
