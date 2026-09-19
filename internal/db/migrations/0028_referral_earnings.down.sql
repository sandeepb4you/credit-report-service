-- Undo migration 0028: drop the referral-earnings tables.
--
-- Earnings and withdrawal history are derived conveniences over attribution,
-- not the attribution itself: accounts.referred_by_* columns are untouched,
-- so rolling this back loses the money state but keeps who-referred-whom.
DROP TABLE IF EXISTS referral_withdrawals;
DROP TABLE IF EXISTS payout_bank_accounts;
DROP TABLE IF EXISTS referral_earnings;
