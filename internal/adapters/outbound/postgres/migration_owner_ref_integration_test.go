//go:build integration

package postgres_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

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

func poolAt(t *testing.T, url string) *pgxpool.Pool {
	t.Helper()
	pool, err := postgres.NewPool(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func exec(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) error {
	t.Helper()
	_, err := pool.Exec(context.Background(), sql, args...)
	return err
}

// TestMigration0004BackfillsOperatorOwnedListings: rows written before 0004
// (a full listing and a suppression stub) land at the empty owner_ref; the key
// (item, node, owner) is unique for every row with an item; 0004 goes down.
func TestMigration0004BackfillsOperatorOwnedListings(t *testing.T) {
	url := pgtest.URL(t)
	if err := postgres.MigrateTo(url, latestMigration-1); err != nil {
		t.Fatalf("down to %d: %v", latestMigration-1, err)
	}
	old := poolAt(t, url)
	for _, row := range [][]any{{"lst-op", "cat-1", "n1", 5}, {"lst-stub", "", "n1", 0}} {
		if err := exec(t, old, `INSERT INTO rm_listings (listing_id, catalog_item_id, node_id, available_qty) VALUES ($1,$2,$3,$4)`, row...); err != nil {
			t.Fatal(err)
		}
	}
	old.Close()
	if err := postgres.MigrateTo(url, latestMigration); err != nil {
		t.Fatalf("up to %d: %v", latestMigration, err)
	}
	pool := poolAt(t, url)
	rm := postgres.NewReadModels(pool)
	for _, id := range []string{"lst-op", "lst-stub"} {
		if l, err := rm.GetListing(context.Background(), id); err != nil || l.OwnerRef != network.OperatorOwned {
			t.Fatalf("backfilled %s = %+v %v, want ownerRef ''", id, l, err)
		}
	}
	wantMigratedKey(t, pool)
	pool.Close()
	if err := postgres.MigrateTo(url, latestMigration-1); err != nil {
		t.Fatalf("0004 down: %v", err)
	}
}

func wantMigratedKey(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ins := `INSERT INTO rm_listings (listing_id, catalog_item_id, node_id, owner_ref, available_qty) VALUES ($1,$2,$3,$4,1)`
	if err := exec(t, pool, ins, "lst-dup", "cat-1", "n1", ""); err == nil {
		t.Fatal("a second listing on the operator key must violate the unique index")
	}
	if err := exec(t, pool, ins, "lst-s1", "cat-1", "n1", "sel-1"); err != nil {
		t.Fatalf("a seller's listing of the same item at the same node: %v", err)
	}
	if err := exec(t, pool, ins, "lst-stub2", "", "n1", ""); err != nil {
		t.Fatalf("a second stub with no item is not a key: %v", err)
	}
}
