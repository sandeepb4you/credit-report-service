-- Agents are referral partners who help users register. Each agent has a unique
-- code that users provide during signup to link their account.
--
-- This migration also adds the agent linkage to the accounts table so both
-- halves are created atomically and the FK resolves within the same step.
CREATE TABLE IF NOT EXISTS agents (
    id          BIGSERIAL   PRIMARY KEY,
    code        VARCHAR(50) NOT NULL UNIQUE,
    name        VARCHAR(255) NOT NULL,
    email       VARCHAR(255),
    phone       VARCHAR(20),
    status      VARCHAR(20) NOT NULL DEFAULT 'ACTIVE',  -- ACTIVE | INACTIVE
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Link accounts to the agent who referred them. agent_code_updated tracks whether
-- the user has already exercised their one-time right to set/change their agent.
ALTER TABLE accounts ADD COLUMN IF NOT EXISTS agent_id          BIGINT          REFERENCES agents(id) ON DELETE SET NULL;
ALTER TABLE accounts ADD COLUMN IF NOT EXISTS agent_code_updated BOOLEAN NOT NULL DEFAULT false;
