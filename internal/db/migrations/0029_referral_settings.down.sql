-- Undo migration 0029: drop the referral-economics row and table.
--
-- referral_earnings rows keep the amount they were credited with (a snapshot,
-- not a reference), so rolling this back leaves history intact and only
-- removes the configurability.
DROP TABLE IF EXISTS referral_settings;
