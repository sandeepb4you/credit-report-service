DROP TABLE IF EXISTS assist_partner_profiles;
DROP TABLE IF EXISTS loan_assist_documents;
DROP TABLE IF EXISTS loan_assist_requests;
DELETE FROM products WHERE code = 'LOAN_ASSIST'
    AND NOT EXISTS (SELECT 1 FROM orders WHERE product_code = 'LOAN_ASSIST');
ALTER TABLE products DROP CONSTRAINT IF EXISTS products_kind_check;
ALTER TABLE products DROP COLUMN IF EXISTS kind;
