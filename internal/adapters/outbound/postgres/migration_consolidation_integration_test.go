//go:build integration

package postgres_test

import (
	"context"
	"testing"

	"github.com/claudioed/order-sourcing/internal/adapters/outbound/postgres"
	"github.com/claudioed/order-sourcing/internal/testing/pgtest"
)

// consolidationMigration is the migration 0006 of ADR 0015.
const consolidationMigration = 6

// TestMigration0006AddsTheConsolidationColumns: a row written before 0006
// reads back as "never merged" (no key, no carrier, no members, no window
// start), the new columns store and 0006 goes down.
func TestMigration0006AddsTheConsolidationColumns(t *testing.T) {
	url := pgtest.URL(t)
	if err := postgres.MigrateTo(url, consolidationMigration-1); err != nil {
		t.Fatalf("down to %d: %v", consolidationMigration-1, err)
	}
	run, _, closeOld := openAt(t, url)
	if err := run(`INSERT INTO assignments (assignment_id, order_id, group_no, destination_postal_prefix, promise_id, tier,
		deliver_by_latest, lines, state, version, queued_at, updated_at)
		VALUES ('asg-old', 'ord-1', 1, '30301', 'prm-1', 'STANDARD', now(), '[]', 'QUEUED', 1, now(), now())`); err != nil {
		t.Fatalf("a row before 0006: %v", err)
	}
	if err := run(`SELECT consolidation_key FROM assignments`); err == nil {
		t.Fatal("the columns must not exist before 0006")
	}
	closeOld()
	if err := postgres.MigrateTo(url, consolidationMigration); err != nil {
		t.Fatalf("up to %d: %v", consolidationMigration, err)
	}
	pool, err := postgres.NewPool(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	var key, into, members string
	var earliestIsNull bool
	if err := pool.QueryRow(context.Background(), `SELECT consolidation_key, consolidated_into, members::text, deliver_by_earliest IS NULL
		FROM assignments WHERE assignment_id = 'asg-old'`).Scan(&key, &into, &members, &earliestIsNull); err != nil {
		t.Fatal(err)
	}
	if key != "" || into != "" || members != "[]" || !earliestIsNull {
		t.Fatalf("old row = %q %q %q %v, want it unmerged", key, into, members, earliestIsNull)
	}
	if _, err := pool.Exec(context.Background(), `UPDATE assignments SET consolidation_key = 'k', consolidated_into = 'asg-c', members = '["asg-2"]', deliver_by_earliest = now()`); err != nil {
		t.Fatalf("the new columns store: %v", err)
	}
	pool.Close()
	if err := postgres.MigrateTo(url, consolidationMigration-1); err != nil {
		t.Fatalf("0006 down: %v", err)
	}
	if err := postgres.MigrateTo(url, latestMigration); err != nil {
		t.Fatalf("back up: %v", err)
	}
}
