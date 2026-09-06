-- Scheduled score checks: one row per future report run a plan purchase bought.
--
-- A myScorr Plus purchase is a single payment for N checks on a cadence.
-- Fulfilment materializes the whole batch up front — row 1 due on the payment
-- day, row i due (i-1) intervals later — so the schedule is durable data, not
-- state derived from counters. The runner's job reduces to "run every PENDING
-- row whose due date has arrived"; a missed day just means the row waits.
--
-- Day granularity on purpose: a run is owed on a DATE ("refresh on 3 Oct"),
-- not at an instant, and the runner executes everything due that day whatever
-- the clock says. Dates are computed in the business timezone (config
-- scheduled-checks.timezone) at write time.
--
-- product_code and interval_months are SNAPSHOTS taken at mint, the same way
-- orders snapshot their price: editing the plan later must never rewrite a
-- batch someone has already paid for.
--
-- "Once per period" is structural: UNIQUE (order_id, sequence_no) means a
-- period exists exactly once, and a DONE row is never eligible again. The
-- runner and the manual-spend path contend over the same rows with
-- FOR UPDATE SKIP LOCKED, so one row funds exactly one report.
CREATE TABLE IF NOT EXISTS scheduled_score_checks (
    id              BIGSERIAL PRIMARY KEY,
    account_id      BIGINT      NOT NULL REFERENCES accounts (id) ON DELETE CASCADE,
    order_id        BIGINT      NOT NULL REFERENCES orders (id),
    product_code    VARCHAR(50) NOT NULL,
    interval_months INT         NOT NULL,
    sequence_no     INT         NOT NULL,
    due_on          DATE        NOT NULL,
    -- Unrun checks past this date are swept to EXPIRED (terms: the plan is
    -- active for the billing year, no proration). NULL = never expires.
    expires_on      DATE,
    -- PENDING -> RUNNING -> DONE, or FAILED (attempt cap reached) / EXPIRED.
    -- RUNNING rows whose last_attempt_at goes stale are reclaimed to PENDING;
    -- the run's idempotency key (sched-<id>) makes the retry safe.
    status          VARCHAR(16) NOT NULL DEFAULT 'PENDING',
    -- Who completed it: 'runner' for the scheduled sweep, 'manual' when the
    -- user spent the row on an on-demand check (which re-anchors the rest of
    -- the batch from that day).
    completed_by    VARCHAR(16),
    report_id       BIGINT REFERENCES credit_analytics_requests (id) ON DELETE SET NULL,
    executed_at     TIMESTAMPTZ,
    attempts        INT         NOT NULL DEFAULT 0,
    last_attempt_at TIMESTAMPTZ,
    failure_reason  TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (order_id, sequence_no)
);

-- The runner's sweep ("what is due") and the entitlement gate's "does this
-- account hold a pending run" both read through these.
CREATE INDEX IF NOT EXISTS idx_scheduled_checks_due
    ON scheduled_score_checks (due_on) WHERE status = 'PENDING';
CREATE INDEX IF NOT EXISTS idx_scheduled_checks_account
    ON scheduled_score_checks (account_id, status, due_on);
