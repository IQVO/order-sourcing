//go:build integration

package postgres_test

import (
	"testing"

	"github.com/claudioed/order-sourcing/internal/adapters/outbound/postgres"
	"github.com/claudioed/order-sourcing/internal/testing/pgtest"
)

// sourcingPoliciesMigration is the migration 0005 of ADR 0014.
const sourcingPoliciesMigration = 5

// TestMigration0005CreatesTheVersionedPolicyTable: before 0005 there is no
// table, after it a version of at least 1 is stored once, and 0005 goes down.
func TestMigration0005CreatesTheVersionedPolicyTable(t *testing.T) {
	url := pgtest.URL(t)
	if err := postgres.MigrateTo(url, sourcingPoliciesMigration-1); err != nil {
		t.Fatalf("down to %d: %v", sourcingPoliciesMigration-1, err)
	}
	run, _, closeOld := openAt(t, url)
	if err := run(`INSERT INTO sourcing_policies (version, snapshot) VALUES (1, '{}')`); err == nil {
		t.Fatal("the table must not exist before 0005")
	}
	closeOld()
	if err := postgres.MigrateTo(url, sourcingPoliciesMigration); err != nil {
		t.Fatalf("up to %d: %v", sourcingPoliciesMigration, err)
	}
	run, _, closeNew := openAt(t, url)
	if err := run(`INSERT INTO sourcing_policies (version, snapshot) VALUES (1, '{}')`); err != nil {
		t.Fatalf("version 1: %v", err)
	}
	if err := run(`INSERT INTO sourcing_policies (version, snapshot) VALUES (1, '{}')`); err == nil {
		t.Fatal("a version is stored once")
	}
	if err := run(`INSERT INTO sourcing_policies (version, snapshot) VALUES (0, '{}')`); err == nil {
		t.Fatal("version 0 is the built-in default, never a row")
	}
	closeNew()
	if err := postgres.MigrateTo(url, sourcingPoliciesMigration-1); err != nil {
		t.Fatalf("0005 down: %v", err)
	}
	if err := postgres.MigrateTo(url, latestMigration); err != nil {
		t.Fatalf("back up: %v", err)
	}
}
