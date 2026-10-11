-- ---------------------------------------------------------------------------
-- admin_dashboards: an operator's own console dashboards.
--
-- A dashboard is a name and an ordered list of widgets, each naming a metric
-- from the server's fixed catalog (service.dashboardMetrics), how to draw it
-- and a relative range ("30d"). The widgets are JSONB because they are only
-- ever read and written whole, and validated by the service on every write —
-- the column is storage, not a schema anyone queries into.
--
-- Private to the account that made it. Sharing is export → import, which
-- copies the definition into the importer's own row rather than linking two
-- operators to one mutable dashboard.
--
-- Nothing personal is stored here: a definition names metrics, never people.
-- ---------------------------------------------------------------------------
CREATE TABLE admin_dashboards (
    id               BIGSERIAL   PRIMARY KEY,
    owner_account_id BIGINT      NOT NULL REFERENCES accounts (id) ON DELETE CASCADE,
    name             VARCHAR(80) NOT NULL CHECK (length(btrim(name)) > 0),
    widgets          JSONB       NOT NULL DEFAULT '[]'::jsonb
                                 CHECK (jsonb_typeof(widgets) = 'array'),
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX admin_dashboards_owner_idx ON admin_dashboards (owner_account_id, updated_at DESC);

-- ---------------------------------------------------------------------------
-- admin_data_exports: the audit trail of CSV downloads from the console.
--
-- One row per export: who, which metric, which days, how many rows left the
-- building. The exports are aggregates (a count or a sum per day) and carry no
-- customer's details, but the revenue line is still not something that should
-- leave unrecorded. Ids and dates only, like admin_impersonations.
-- ---------------------------------------------------------------------------
CREATE TABLE admin_data_exports (
    id         BIGSERIAL   PRIMARY KEY,
    account_id BIGINT      NOT NULL REFERENCES accounts (id),
    metric     VARCHAR(40) NOT NULL,
    range_from DATE        NOT NULL,
    range_to   DATE        NOT NULL,
    row_count  INTEGER     NOT NULL CHECK (row_count >= 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (range_to >= range_from)
);

CREATE INDEX admin_data_exports_account_idx ON admin_data_exports (account_id, created_at DESC);
