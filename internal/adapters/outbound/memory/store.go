// Package memory holds in-memory TEST DOUBLES of the application's out ports.
// They are for unit, BDD and contract tests only: cmd/** never imports this
// package (enforced by internal/architecture) because the service runs on real
// Postgres and Kafka.
//
// Every repository round-trips its aggregate through Snapshot and Restore, so a
// loaded aggregate never aliases stored state, a corrupt row can not become a
// live aggregate and the doubles keep the persistence contract of the real
// adapters: a miss is ports.ErrNotFound, a stale or duplicate save is
// ports.ErrConcurrentUpdate, a second assignment of one (orderId, groupNo) is
// ports.ErrDuplicateGroup.
//
// One Store backs every double. Its UnitOfWork is atomic: it holds the store
// for the whole unit and restores the state taken at the start when the unit
// fails or panics, outbox and inbox included.
package memory

import (
	"context"
	"sync"

	"github.com/claudioed/order-sourcing/internal/domain/assignment"
	"github.com/claudioed/order-sourcing/internal/domain/network"
	"github.com/claudioed/order-sourcing/internal/domain/shared"
)

type scorecardKey struct{ node, window string }

// state is everything a unit of work can change. Stored snapshots and rows are
// replaced, never edited in place, so copying the maps is enough.
type state struct {
	assignments  map[assignment.ID]assignment.Snapshot
	nodes        map[string]network.Node
	lanes        map[string]network.Lane
	departures   map[string]network.Departure
	waves        map[string]network.Wave
	areas        map[string]network.ServiceArea
	regions      map[string]network.Region
	listings     map[string]network.Listing
	discontinued map[string]bool
	scorecards   map[scorecardKey]network.Scorecard
	pressure     map[string]network.Pressure
	inbox        map[string]string
	outbox       []shared.Event
}

// Store is the shared state of the in-memory doubles.
type Store struct {
	mu sync.Mutex
	state
}

func newState() state {
	return state{
		assignments: map[assignment.ID]assignment.Snapshot{}, nodes: map[string]network.Node{}, lanes: map[string]network.Lane{},
		departures: map[string]network.Departure{}, waves: map[string]network.Wave{}, areas: map[string]network.ServiceArea{},
		regions: map[string]network.Region{}, listings: map[string]network.Listing{}, discontinued: map[string]bool{},
		scorecards: map[scorecardKey]network.Scorecard{}, pressure: map[string]network.Pressure{}, inbox: map[string]string{},
	}
}

// NewStore returns an empty store.
func NewStore() *Store { return &Store{state: newState()} }

func cloneMap[K comparable, V any](m map[K]V) map[K]V {
	out := make(map[K]V, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func (s *Store) capture() state {
	return state{
		assignments: cloneMap(s.assignments), nodes: cloneMap(s.nodes), lanes: cloneMap(s.lanes), departures: cloneMap(s.departures),
		waves: cloneMap(s.waves), areas: cloneMap(s.areas), regions: cloneMap(s.regions), listings: cloneMap(s.listings),
		discontinued: cloneMap(s.discontinued), scorecards: cloneMap(s.scorecards), pressure: cloneMap(s.pressure),
		inbox: cloneMap(s.inbox), outbox: append([]shared.Event(nil), s.outbox...),
	}
}

// txKey marks a ctx that carries the open unit of work of one store.
type txKey struct{ store *Store }

// enter locks the store for one repository call, unless ctx already carries the
// unit of work that holds it. The returned func releases the lock.
func (s *Store) enter(ctx context.Context) func() {
	if ctx.Value(txKey{s}) != nil {
		return func() {}
	}
	s.mu.Lock()
	return s.mu.Unlock
}

// Assignments returns the AssignmentRepository double.
func (s *Store) Assignments() *AssignmentRepository { return &AssignmentRepository{s} }

// ReadModels returns the ReadModels double.
func (s *Store) ReadModels() *ReadModels { return &ReadModels{s} }

// World returns the WorldReader double.
func (s *Store) World() *WorldReader { return &WorldReader{s} }

// Outbox returns the EventOutbox double.
func (s *Store) Outbox() *Outbox { return &Outbox{s} }

// Inbox returns the Inbox double.
func (s *Store) Inbox() *Inbox { return &Inbox{s} }

// UnitOfWork returns the UnitOfWork double.
func (s *Store) UnitOfWork() *UnitOfWork { return &UnitOfWork{s} }

// Counts is how many rows each table of the store holds.
type Counts struct {
	Assignments, Nodes, Departures, Waves, Listings, InboxRows, OutboxEvents int
	// VersionSum is the sum of every assignment version: any saved change moves it.
	VersionSum int64
}

// Counts returns the size of every table and the sum of the assignment
// versions, so a test can prove that a failed use case stored and changed nothing.
func (s *Store) Counts() Counts {
	s.mu.Lock()
	defer s.mu.Unlock()
	var versions int64
	for _, a := range s.assignments {
		versions += a.Version
	}
	return Counts{
		Assignments: len(s.assignments), Nodes: len(s.nodes), Departures: len(s.departures), Waves: len(s.waves),
		Listings: len(s.listings), InboxRows: len(s.inbox), OutboxEvents: len(s.outbox), VersionSum: versions,
	}
}
