// Package repocontract is the persistence contract of the out ports: one suite
// that runs against the in-memory doubles and against Postgres, so both keep
// the same rules (a miss is ports.ErrNotFound, a stale save is
// ports.ErrConcurrentUpdate, a unit of work is atomic, the world snapshot is
// the slice one search needs). It imports "testing" on purpose: it is test
// support, never linked into cmd/**.
package repocontract

import (
	"context"
	"testing"
	"time"

	"github.com/claudioed/order-sourcing/internal/application/ports"
)

// Fixture is what an adapter hands the suite. Fresh returns a new, empty
// environment; the suite never shares state between subtests.
type Fixture struct {
	Assignments ports.AssignmentRepository
	ReadModels  ports.ReadModels
	World       ports.WorldReader
	Inbox       ports.Inbox
	Outbox      ports.EventOutbox
	UoW         ports.UnitOfWork
	// OutboxCount returns how many events the outbox holds.
	OutboxCount func(t *testing.T) int
}

// Factory builds a fresh Fixture.
type Factory func(t *testing.T) Fixture

// T0 is the instant the suite builds its data around, in UTC and exact to the
// microsecond (the precision of Postgres).
var T0 = time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)

// Run executes the whole contract.
func Run(t *testing.T, f Factory) {
	t.Helper()
	t.Run("assignments", func(t *testing.T) { runAssignments(t, f) })
	t.Run("read models", func(t *testing.T) { runReadModels(t, f) })
	t.Run("world", func(t *testing.T) { runWorld(t, f) })
	t.Run("unit of work", func(t *testing.T) { runUnitOfWork(t, f) })
}

func ctx() context.Context { return context.Background() }
