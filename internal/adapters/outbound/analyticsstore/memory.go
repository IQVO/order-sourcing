package analyticsstore

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/claudioed/order-sourcing/internal/analytics/report"
)

// MemoryStore is the in-memory twin of Projection and Reader, for unit tests
// of the consumer and the reports. contract_test.go runs the SAME suite
// against it and against Postgres, so its behaviour cannot drift from the real
// store. The shipped binaries never construct it.
type MemoryStore struct {
	mu           sync.Mutex
	processed    map[string]time.Time
	queued       map[string]memQueued
	decisions    map[decisionKey]memDecision
	unsourceable []time.Time
	// Fail, when set, is returned by the next Apply (and cleared), to drive
	// the consumer's transient and rejection paths.
	Fail error
}

type memQueued struct {
	order   string
	groupNo int
	at      time.Time
}

type decisionKey struct {
	assignment string
	releaseNo  int
}

type memDecision struct {
	report.Decision
	at time.Time
}

// NewMemoryStore returns an empty twin.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{processed: map[string]time.Time{}, queued: map[string]memQueued{}, decisions: map[decisionKey]memDecision{}}
}

var (
	_ report.Projection = (*MemoryStore)(nil)
	_ report.Reader     = (*MemoryStore)(nil)
)

// Apply implements report.Projection with the Postgres store's semantics: an
// event the schema's CHECKs would refuse is rejected before anything is
// recorded, a replay is a no-op.
func (m *MemoryStore) Apply(_ context.Context, e report.Event) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Fail != nil {
		err := m.Fail
		m.Fail = nil
		return false, err
	}
	if err := e.Validate(); err != nil {
		return false, fmt.Errorf("%w: %w", report.ErrRejected, err)
	}
	if _, seen := m.processed[e.EventID]; seen {
		return false, nil
	}
	m.processed[e.EventID] = e.At.UTC()
	switch e.Kind {
	case report.KindFulfillmentQueued:
		if _, ok := m.queued[e.AssignmentID]; !ok {
			m.queued[e.AssignmentID] = memQueued{order: e.OrderID, groupNo: e.GroupNo, at: e.OccurredAt.UTC()}
		}
	case report.KindFulfillmentDecided:
		m.decisions[decisionKey{e.AssignmentID, e.Decision.ReleaseNo}] = memDecision{Decision: e.Decision, at: e.OccurredAt.UTC()}
	default: // Validate admitted only the three kinds
		m.unsourceable = append(m.unsourceable, e.OccurredAt.UTC())
	}
	return true, nil
}

func utcDay(t time.Time) time.Time { return t.UTC().Truncate(24 * time.Hour) }

func inRange(r report.Range, at time.Time) bool { return at.Compare(r.From) >= 0 && at.Before(r.To) }

// dayOf returns the row of day, creating it in rows.
func dayOf[T any](rows map[time.Time]*T, day time.Time, init func(time.Time) *T) *T {
	if d, ok := rows[day]; ok {
		return d
	}
	d := init(day)
	rows[day] = d
	return d
}

// sortedDays returns the rows ordered by day.
func sortedDays[T any](rows map[time.Time]*T, day func(T) time.Time) []T {
	out := make([]T, 0, len(rows))
	for _, d := range rows {
		out = append(out, *d)
	}
	sort.Slice(out, func(i, j int) bool { return day(out[i]).Before(day(out[j])) })
	return out
}
