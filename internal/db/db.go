// Package db wires the Postgres connection pool and runs embedded migrations
// at startup. It is the single point of access to *pgxpool.Pool for the rest
// of the service.
package db

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"time"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/source/iofs"
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
	dsn := cfg.DSN
	if dsn == "" {
		// Build a key=value DSN from URL + creds when an explicit DSN isn't set.
		// pgxpool accepts both URL form and keyword form.
		if cfg.URL != "" {
			dsn = cfg.URL
		} else {
			dsn = fmt.Sprintf(
				"postgres://%s:%s@localhost:5432/credit_report?currentSchema=credit_report",
				cfg.Username, cfg.Password,
			)
		}
	}

	poolCfg, err := pgxpool.ParseConfig(dsn)
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
	dsn := cfg.DSN
	if dsn == "" {
		if cfg.URL != "" {
			dsn = cfg.URL
		} else {
			dsn = fmt.Sprintf(
				"postgres://%s:%s@localhost:5432/credit_report?currentSchema=credit_report",
				cfg.Username, cfg.Password,
			)
		}
	}

	src, err := iofs.New(migrationsFS, "migrations")
	if err != nil {
		return fmt.Errorf("load migration source: %w", err)
	}
	m, err := migrate.NewWithSourceInstance("iofs", src, dsn)
	if err != nil {
		return fmt.Errorf("init migrate: %w", err)
	}
	defer m.Close()

	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("apply migrations: %w", err)
	}
	return nil
}
