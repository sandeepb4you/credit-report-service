-- ---------------------------------------------------------------------------
-- orders.payment_mode: which Cashfree environment an order was created in.
--
-- Until now there was one gateway for the whole deployment, so "the mode" was
-- a property of the server. It becomes a property of the ORDER: the store app
-- and the web pay live, while internal builds (the APK shared on WhatsApp)
-- keep paying in sandbox. Everything that happens to an order after it is
-- created — reconciling it, accepting its webhook, crediting a referral off
-- it — has to go back to the environment that created it, and after this
-- migration that is read off the row rather than off the config.
--
-- Two properties matter and the column is what gives them:
--
--   * A webhook signed with the SANDBOX secret must never settle a
--     PRODUCTION order. Sandbox "payments" are free to make; if one could mark
--     a live order paid, anyone with a test build would have a way to get real
--     bureau pulls — which cost real money upstream — for nothing.
--   * Orders created before a switch keep reconciling against the environment
--     they were created in. Without the column, flipping the deployment to
--     production would send every still-open sandbox order to the production
--     API, which has never heard of it.
--
-- Every order that exists today was created with sandbox credentials (the
-- deployment has only ever had TEST keys), so the backfill is exactly right.
-- The default is then DROPPED: a new insert that forgets to say which
-- environment it used should fail loudly at the NOT NULL, not be guessed.
-- ---------------------------------------------------------------------------
ALTER TABLE orders
    ADD COLUMN payment_mode VARCHAR(16) NOT NULL DEFAULT 'sandbox'
        CHECK (payment_mode IN ('sandbox', 'production'));

ALTER TABLE orders ALTER COLUMN payment_mode DROP DEFAULT;
