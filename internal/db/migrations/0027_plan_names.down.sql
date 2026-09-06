-- Restore the names and the checklist line migration 0022 seeded.
UPDATE products SET name = 'myScorr Starter Plan', updated_at = now()
 WHERE code = 'CREDIT_ANALYSIS';

UPDATE products SET name        = 'myScorr Plus Quarterly',
                    description = replace(description, 'myScorr Starter', 'myScorr Starter Plan'),
                    updated_at  = now()
 WHERE code = 'SCORE_PLUS_QUARTERLY';

UPDATE products SET name = 'myScorr Plus Monthly', updated_at = now()
 WHERE code = 'SCORE_PLUS_MONTHLY';

-- Put the WhatsApp line back where 0022 had it: second, under "Refresh every
-- month". Guarded on its absence so a re-run cannot duplicate it.
UPDATE products
   SET description = replace(description,
                             'Refresh every month',
                             'Refresh every month' || chr(10) || 'Instant WhatsApp change alerts'),
       updated_at  = now()
 WHERE code = 'SCORE_PLUS_MONTHLY'
   AND description NOT LIKE '%WhatsApp%';
