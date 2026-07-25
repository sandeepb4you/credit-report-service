-- Drop the credit_reports table.
--
-- The credit-reports REST API has been removed; the table was created in
-- migration 0001_init.up.sql. No foreign keys from other tables reference it
-- (verified before writing this migration), so a plain DROP is safe.
--
-- This is destructive: any stored report rows are deleted. The down migration
-- can recreate the table structure but cannot restore the data.
DROP TABLE IF EXISTS credit_reports;
