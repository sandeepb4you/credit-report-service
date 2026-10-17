-- The 'agent' role is renamed 'partner'. Partners still issue coupon codes and
-- now also work Loan Assist applications. Access tokens already issued carry
-- 'agent' in their role claim until they expire; models.NormalizeRole reads
-- that as 'partner', so nobody is locked out by the rename.
UPDATE accounts SET role = 'partner', updated_at = now() WHERE role = 'agent';
