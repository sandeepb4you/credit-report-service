DROP INDEX IF EXISTS idx_bank_statements_request_id;
CREATE INDEX idx_bank_statements_request_id
    ON bank_statements (request_id)
    WHERE request_id IS NOT NULL;

-- Digitap rows carry no PDF and cannot satisfy the restored NOT NULL.
DELETE FROM bank_statements WHERE pdf_bytes IS NULL;

ALTER TABLE bank_statements
    DROP CONSTRAINT IF EXISTS bank_statements_local_has_pdf,
    ALTER COLUMN pdf_bytes SET NOT NULL,
    ALTER COLUMN filename DROP DEFAULT;
