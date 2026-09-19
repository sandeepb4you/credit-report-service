-- Referral programme economics, editable by an admin instead of constants.
--
-- One row, id = 1, never inserted or deleted by the app — only updated:
-- reward_paise is the flat credit per converted referral (a referred account's
-- first PAID order writes exactly this into referral_earnings, so a change
-- applies to later conversions and never rewrites history);
-- min_withdrawal_paise is the smallest request POST /referrals/withdrawals
-- accepts. Both are paise (12500 = Rs 125, 50000 = Rs 500).
CREATE TABLE referral_settings (
    id                   INTEGER PRIMARY KEY CHECK (id = 1),
    reward_paise         INTEGER NOT NULL CHECK (reward_paise > 0),
    min_withdrawal_paise INTEGER NOT NULL CHECK (min_withdrawal_paise > 0),
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_by_account_id BIGINT REFERENCES accounts (id) ON DELETE SET NULL
);

INSERT INTO referral_settings (id, reward_paise, min_withdrawal_paise)
VALUES (1, 12500, 50000);
