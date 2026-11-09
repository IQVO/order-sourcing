//go:build integration

package postgres_test

import (
	"context"
	"testing"

	"github.com/claudioed/order-sourcing/internal/adapters/outbound/postgres"
	"github.com/claudioed/order-sourcing/internal/domain/network"
	"github.com/claudioed/order-sourcing/internal/testing/pgtest"
)

// latestMigration is the version ceiling of the embedded migrations: 0004 adds
// rm_listings.owner_ref (ADR 0012). A new migration moves it.
const latestMigration = 4

func TestMigrationsReachTheLatestVersion(t *testing.T) {
	steps, err := postgres.MigrationSteps()
	if err != nil || steps != latestMigration {
		t.Fatalf("embedded up migrations = %d %v, want %d", steps, err, latestMigration)
	}
	url := pgtest.URL(t)
	v, dirty, err := postgres.MigrationVersion(url)
	if err != nil || dirty || v != latestMigration {
		t.Fatalf("version = %d dirty=%v %v, want %d clean", v, dirty, err, latestMigration)
	}
}

// sqlExec runs one statement on a database of the shared pgtest container.
type sqlExec func(sql string, args ...any) error

// openAt opens a pool on url (the pgtest container) and returns a statement
// runner, the read models on it and the close func.
func openAt(t *testing.T, url string) (sqlExec, *postgres.ReadModels, func()) {
	t.Helper()
	pool, err := postgres.NewPool(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	run := func(sql string, args ...any) error {
		_, err := pool.Exec(context.Background(), sql, args...)
		return err
	}
	return run, postgres.NewReadModels(pool), pool.Close
}

// TestMigration0004BackfillsOperatorOwnedListings: rows written before 0004
// (a full listing and a suppression stub) land at the empty owner_ref; the key
// (item, node, owner) is unique for every row with an item; 0004 goes down.
func TestMigration0004BackfillsOperatorOwnedListings(t *testing.T) {
	url := pgtest.URL(t)
	if err := postgres.MigrateTo(url, latestMigration-1); err != nil {
		t.Fatalf("down to %d: %v", latestMigration-1, err)
	}
	run, _, closeOld := openAt(t, url)
	for _, row := range [][]any{{"lst-op", "cat-1", "n1", 5}, {"lst-stub", "", "n1", 0}} {
		if err := run(`INSERT INTO rm_listings (listing_id, catalog_item_id, node_id, available_qty) VALUES ($1,$2,$3,$4)`, row...); err != nil {
			t.Fatal(err)
		}
	}
	closeOld()
	if err := postgres.MigrateTo(url, latestMigration); err != nil {
		t.Fatalf("up to %d: %v", latestMigration, err)
	}
	run, rm, closeNew := openAt(t, url)
	for _, id := range []string{"lst-op", "lst-stub"} {
		if l, err := rm.GetListing(context.Background(), id); err != nil || l.OwnerRef != network.OperatorOwned {
			t.Fatalf("backfilled %s = %+v %v, want the empty ownerRef", id, l, err)
		}
	}
	wantMigratedKey(t, run)
	closeNew()
	if err := postgres.MigrateTo(url, latestMigration-1); err != nil {
		t.Fatalf("0004 down: %v", err)
	}
}

func wantMigratedKey(t *testing.T, run sqlExec) {
	t.Helper()
	ins := `INSERT INTO rm_listings (listing_id, catalog_item_id, node_id, owner_ref, available_qty) VALUES ($1,$2,$3,$4,1)`
	if err := run(ins, "lst-dup", "cat-1", "n1", ""); err == nil {
		t.Fatal("a second listing on the operator key must violate the unique index")
	}
	if err := run(ins, "lst-s1", "cat-1", "n1", "sel-1"); err != nil {
		t.Fatalf("a seller's listing of the same item at the same node: %v", err)
	}
	if err := run(ins, "lst-stub2", "", "n1", ""); err != nil {
		t.Fatalf("a second stub with no item is not a key: %v", err)
	}
}
