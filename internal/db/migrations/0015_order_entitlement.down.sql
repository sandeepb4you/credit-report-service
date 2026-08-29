DROP INDEX IF EXISTS idx_orders_entitlement;

ALTER TABLE orders
    DROP COLUMN IF EXISTS consumed_report_id,
    DROP COLUMN IF EXISTS consumed_at;
