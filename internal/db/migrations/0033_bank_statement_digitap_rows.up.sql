-- ---------------------------------------------------------------------------
-- bank_statements: let a Digitap row exist.
--
-- 0006 added the Digitap columns but left 0005's upload-shaped constraints in
-- place, so every POST /bank-statements/digitap/initiate failed its insert:
-- a Digitap row has no file on our side (the user uploads to Digitap's UI),
-- hence no filename and no pdf_bytes, and both were NOT NULL. The endpoint
-- has never successfully created a row.
--
-- filename keeps NOT NULL with an empty default (the model holds it as a
-- string). pdf_bytes becomes nullable, with the requirement kept where it
-- belongs: a local row is exactly an uploaded file, so it must carry one.
--
-- request_id's index becomes UNIQUE: the public callback finds the row by
-- request_id alone, and two rows answering to one would let a callback act on
-- the wrong account's upload.
-- ---------------------------------------------------------------------------
ALTER TABLE bank_statements
    ALTER COLUMN filename SET DEFAULT '',
    ALTER COLUMN pdf_bytes DROP NOT NULL,
    ADD CONSTRAINT bank_statements_local_has_pdf
        CHECK (provider <> 'local' OR pdf_bytes IS NOT NULL);

DROP INDEX IF EXISTS idx_bank_statements_request_id;
CREATE UNIQUE INDEX idx_bank_statements_request_id
    ON bank_statements (request_id)
    WHERE request_id IS NOT NULL;
