//go:build integration

package postgres

import (
	"errors"
	"fmt"
	"io/fs"
	"strings"

	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/source/iofs"
)

// MigrateTo moves the schema of databaseURL to migration version v, up or down
// (test-only: the migration integration tests step through the versions).
func MigrateTo(databaseURL string, v uint) error {
	src, err := iofs.New(migrationsFS, "migrations")
	if err != nil {
		return fmt.Errorf("open embedded migrations: %w", err)
	}
	m, err := migrate.NewWithSourceInstance("iofs", src, databaseURL)
	if err != nil {
		return err
	}
	defer func() { _, _ = m.Close() }()
	if err := m.Migrate(v); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return err
	}
	return nil
}

// MigrationVersion reports the schema version of databaseURL and whether it is dirty.
func MigrationVersion(databaseURL string) (uint, bool, error) {
	src, err := iofs.New(migrationsFS, "migrations")
	if err != nil {
		return 0, false, fmt.Errorf("open embedded migrations: %w", err)
	}
	m, err := migrate.NewWithSourceInstance("iofs", src, databaseURL)
	if err != nil {
		return 0, false, err
	}
	defer func() { _, _ = m.Close() }()
	return m.Version()
}

// MigrationSteps counts the embedded up migrations.
func MigrationSteps() (int, error) {
	entries, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		return 0, err
	}
	n := 0
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".up.sql") {
			n++
		}
	}
	return n, nil
}
