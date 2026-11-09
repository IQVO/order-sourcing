// Package analyticsstore holds the adapters of the analytics read side (ADR
// 0013) over the SEPARATE analytical database: Projection (the writer used by
// cmd/os-projector), Reader (the read-only report queries used by
// cmd/os-reports), the embedded analytical migrations, and an in-memory twin
// of both (MemoryStore) that one contract suite holds to the same behaviour.
// No OLTP package may import this one, and it imports nothing internal but the
// analytics read model (arch test).
package analyticsstore

import (
	"context"
	"errors"
	"fmt"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres" // the postgres:// driver
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/claudioed/order-sourcing/analytics"
)

const (
	// maxConns is the projector's connection ceiling: one consumer loop
	// issuing single-event transactions needs very few.
	maxConns = 5
	// reportsMaxConns is the reports process's ceiling (stateless reads).
	reportsMaxConns = 5
	// statementTimeout bounds one projector statement.
	statementTimeout = "10s"
	// reportsStatementTimeout bounds one report query: aggregates over a
	// caller-chosen range (at most 366 days) get more headroom than a
	// single-row insert, but never run unbounded.
	reportsStatementTimeout = "15s"
)

// NewPool opens the WRITER pool over the analytical database.
func NewPool(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	return newPool(ctx, databaseURL, maxConns, statementTimeout, false)
}

// NewReadOnlyPool opens the READER pool: every session defaults to read-only
// transactions, so a bug in cmd/os-reports cannot mutate the model even if its
// database role could (defence in depth on top of the role).
func NewReadOnlyPool(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	return newPool(ctx, databaseURL, reportsMaxConns, reportsStatementTimeout, true)
}

func newPool(ctx context.Context, databaseURL string, conns int32, timeout string, readOnly bool) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("analyticsstore: parse dsn: %w", err)
	}
	cfg.MaxConns = conns
	if readOnly {
		if cfg.ConnConfig.RuntimeParams == nil {
			cfg.ConnConfig.RuntimeParams = map[string]string{}
		}
		cfg.ConnConfig.RuntimeParams["default_transaction_read_only"] = "on"
	}
	cfg.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		_, err := conn.Exec(ctx, "SET statement_timeout = '"+timeout+"'")
		return err
	}
	return pgxpool.NewWithConfig(ctx, cfg)
}

// RunMigrations applies every pending embedded analytical migration against
// databaseURL (a direct DSN: golang-migrate takes a session lock).
func RunMigrations(databaseURL string) error {
	m, err := migrator(databaseURL)
	if err != nil {
		return err
	}
	defer func() { _, _ = m.Close() }()
	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("analyticsstore: migrate up: %w", err)
	}
	return nil
}

// RollbackMigrations applies every embedded down migration. The binaries never
// call it; the integration tests prove the schema goes up, down and up again.
func RollbackMigrations(databaseURL string) error {
	m, err := migrator(databaseURL)
	if err != nil {
		return err
	}
	defer func() { _, _ = m.Close() }()
	if err := m.Down(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("analyticsstore: migrate down: %w", err)
	}
	return nil
}

func migrator(databaseURL string) (*migrate.Migrate, error) {
	src, err := iofs.New(analytics.Migrations, "migrations")
	if err != nil {
		return nil, fmt.Errorf("analyticsstore: open embedded migrations: %w", err)
	}
	m, err := migrate.NewWithSourceInstance("iofs", src, databaseURL)
	if err != nil {
		return nil, fmt.Errorf("analyticsstore: migrations: %w", err)
	}
	return m, nil
}
