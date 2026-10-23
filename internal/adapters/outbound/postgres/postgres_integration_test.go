//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/claudioed/order-sourcing/internal/adapters/outbound/kafka"
	"github.com/claudioed/order-sourcing/internal/adapters/outbound/postgres"
	"github.com/claudioed/order-sourcing/internal/adapters/outbound/repocontract"
	"github.com/claudioed/order-sourcing/internal/application/ports"
	"github.com/claudioed/order-sourcing/internal/domain/assignment"
	"github.com/claudioed/order-sourcing/internal/testing/pgtest"
)

func TestMain(m *testing.M) {
	code := m.Run()
	pgtest.Shutdown()
	os.Exit(code)
}

// TestPostgresKeepsThePersistenceContract runs the SAME suite as the in-memory doubles.
func TestPostgresKeepsThePersistenceContract(t *testing.T) {
	repocontract.Run(t, func(t *testing.T) repocontract.Fixture {
		pool := pgtest.NewPool(t)
		return repocontract.Fixture{
			Assignments: postgres.NewAssignmentRepository(pool), ReadModels: postgres.NewReadModels(pool), World: postgres.NewWorldReader(pool),
			Inbox: postgres.NewInbox(pool), Outbox: postgres.NewOutbox(pool, kafka.Encoder{}), UoW: postgres.NewUnitOfWork(pool),
			OutboxCount: func(t *testing.T) int { return pgtest.CountRows(t, pool, "outbox_events") },
		}
	})
}

func TestMigrationsGoUpDownUp(t *testing.T) {
	url := pgtest.URL(t)
	if err := postgres.MigrateDown(url); err != nil {
		t.Fatalf("down: %v", err)
	}
	if err := postgres.RunMigrations(url); err != nil {
		t.Fatalf("up again: %v", err)
	}
	if err := postgres.RunMigrations(url); err != nil {
		t.Fatalf("a second up is a no-op: %v", err)
	}
}

func TestDuplicateGroupAndPrimaryKeyAreDistinguished(t *testing.T) {
	pool := pgtest.NewPool(t)
	repo := postgres.NewAssignmentRepository(pool)
	mk := func(id string, group int) *assignment.Assignment {
		a, _, err := assignment.Queue(assignment.New{
			ID: assignment.ID(id), OrderID: "ord-1", GroupNo: group, DestinationPostalPrefix: "30301",
			Promise: assignment.PromiseConstraint{PromiseID: "prm-" + id, Tier: assignment.TierTwoDay, DeliverByLatest: repocontract.T0.Add(60 * 3600e9)},
			Lines:   []assignment.Line{{LineNo: 1, CatalogItemID: "cat-1", Quantity: 1}},
		}, nil, repocontract.T0, repocontract.T0)
		if err != nil {
			t.Fatal(err)
		}
		return a
	}
	if err := repo.Save(context.Background(), mk("asg-1", 1)); err != nil {
		t.Fatal(err)
	}
	if err := repo.Save(context.Background(), mk("asg-2", 1)); !errors.Is(err, ports.ErrDuplicateGroup) {
		t.Fatalf("same group, other id = %v", err)
	}
	if err := repo.Save(context.Background(), mk("asg-1", 2)); !errors.Is(err, ports.ErrConcurrentUpdate) {
		t.Fatalf("same id = %v", err)
	}
}
