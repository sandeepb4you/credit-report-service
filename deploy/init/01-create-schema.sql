-- Runs once when the pgdata volume is first created. DB_URL pins
-- search_path=report, so the schema must exist before the app's embedded
-- migrations run.
CREATE SCHEMA IF NOT EXISTS report;
