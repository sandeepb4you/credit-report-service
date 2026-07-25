// Package db wires the Postgres connection pool and runs embedded migrations
// at startup. It is the single point of access to *pgxpool.Pool for the rest
// of the service.
package db

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"credit-report-service/internal/config"
)

// migrationsFS is the embedded migration source. The .sql files live alongside
// this file in ./migrations and are embedded at compile time, so the binary is
// self-contained.
//
//go:embed migrations/*.sql
var migrationsFS embed.FS

// New constructs a pgxpool, pings it, and returns it. The caller is responsible
// for closing it on shutdown.
func New(ctx context.Context, cfg config.DBConfig) (*pgxpool.Pool, error) {
	dsn := resolveDSN(cfg)

	// pgxpool.ParseConfig only understands the postgres:// /postgresql:// scheme,
	// so normalize away any golang-migrate scheme alias (pgx://, pgx5://).
	dsnForPool := withScheme(dsn, "postgres")

	poolCfg, err := pgxpool.ParseConfig(dsnForPool)
	if err != nil {
		return nil, fmt.Errorf("parse db dsn: %w", err)
	}
	if cfg.MaxPoolSize > 0 {
		poolCfg.MaxConns = int32(cfg.MaxPoolSize)
	}
	if cfg.MinIdle > 0 {
		poolCfg.MinConns = int32(cfg.MinIdle)
	}

	pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	pool, err := pgxpool.NewWithConfig(pingCtx, poolCfg)
	if err != nil {
		return nil, fmt.Errorf("create db pool: %w", err)
	}
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping db: %w", err)
	}
	return pool, nil
}

// Migrate applies all pending up migrations embedded in the binary.
// Safe to call on every boot — applied migrations are skipped.
func Migrate(ctx context.Context, cfg config.DBConfig) error {
	dsn := resolveDSN(cfg)

	// The target schema must exist before golang-migrate initializes: its pgx
	// driver runs SELECT CURRENT_SCHEMA(), which returns NULL when the search_path
	// points at a missing schema, and scanning NULL into a string fails. A
	// migration can't create the schema itself (chicken-and-egg), so do it here.
	if err := ensureSchema(ctx, dsn, schemaFromDSN(dsn)); err != nil {
		return err
	}

	// golang-migrate's pgx/v5 driver registers under the scheme "pgx5", so the
	// database URL passed to it must use that scheme regardless of what the
	// config file uses (postgres://, pgx://, or pgx5://).
	dsnForMigrate := withScheme(dsn, "pgx5")

	src, err := iofs.New(migrationsFS, "migrations")
	if err != nil {
		return fmt.Errorf("load migration source: %w", err)
	}
	m, err := migrate.NewWithSourceInstance("iofs", src, dsnForMigrate)
	if err != nil {
		return fmt.Errorf("init migrate: %w", err)
	}
	defer m.Close()

	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("apply migrations: %w", err)
	}
	return nil
}

// ensureSchema creates the target schema if it doesn't already exist. It opens
// a throwaway pool (not bound to search_path) so CREATE SCHEMA works even on a
// fresh database, then closes it.
func ensureSchema(ctx context.Context, dsn, schema string) error {
	if schema == "" {
		return nil
	}

	// Strip the search_path param so CURRENT_SCHEMA() resolves to the default
	// (existing) schema and we don't trip the same NULL bug while connecting.
	cfg, err := pgxpool.ParseConfig(withScheme(stripSearchPath(dsn), "postgres"))
	if err != nil {
		return fmt.Errorf("parse dsn for schema bootstrap: %w", err)
	}
	cfg.MaxConns = 1

	ensureCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	pool, err := pgxpool.NewWithConfig(ensureCtx, cfg)
	if err != nil {
		return fmt.Errorf("open bootstrap pool: %w", err)
	}
	defer pool.Close()

	// CREATE SCHEMA IF NOT EXISTS is idempotent and safe to run on every boot.
	// pgx.Identifier.Sanitize() quotes the name to protect against injection and
	// to handle reserved/cased identifiers correctly.
	stmt := fmt.Sprintf("CREATE SCHEMA IF NOT EXISTS %s", pgx.Identifier{schema}.Sanitize())
	if _, err := pool.Exec(ensureCtx, stmt); err != nil {
		return fmt.Errorf("create schema %q: %w", schema, err)
	}
	return nil
}

// schemaFromDSN extracts the "search_path" query param from the DSN, falling
// back to "report" to match the service default.
func schemaFromDSN(dsn string) string {
	s := withScheme(dsn, "postgres")
	u, err := url.Parse(s)
	if err != nil {
		return "report"
	}
	if v := u.Query().Get("search_path"); v != "" {
		return v
	}
	return "report"
}

// stripSearchPath removes the search_path query param from the DSN URL form.
// If the DSN isn't a URL, it is returned unchanged.
func stripSearchPath(dsn string) string {
	s := dsn
	for _, scheme := range []string{"postgres://", "postgresql://", "pgx://", "pgx5://"} {
		if strings.HasPrefix(s, scheme) {
			u, err := url.Parse(s)
			if err != nil {
				return dsn
			}
			q := u.Query()
			q.Del("search_path")
			u.RawQuery = q.Encode()
			return u.String()
		}
	}
	return dsn
}

// resolveDSN picks the DSN from cfg.DSN, then cfg.URL, falling back to a
// default URL built from the configured credentials.
func resolveDSN(cfg config.DBConfig) string {
	if cfg.DSN != "" {
		return cfg.DSN
	}
	if cfg.URL != "" {
		return cfg.URL
	}
	return fmt.Sprintf(
		"postgres://%s:%s@localhost:5432/credit?search_path=report",
		cfg.Username, cfg.Password,
	)
}

// withScheme rewrites the scheme of a URL string. It handles the cases that
// show up in config files: "postgres://", "postgresql://", "pgx://", and
// "pgx5://". Anything that doesn't look like one of those is returned as-is so
// key=value DSNs (which pgxpool also accepts) are left untouched.
func withScheme(dsn, scheme string) string {
	for _, s := range []string{"postgres://", "postgresql://", "pgx://", "pgx5://"} {
		if strings.HasPrefix(dsn, s) {
			return scheme + "://" + dsn[len(s):]
		}
	}
	return dsn
}
