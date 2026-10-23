package repocontract

import (
	"testing"

	"github.com/claudioed/order-sourcing/internal/domain/assignment"
	"github.com/claudioed/order-sourcing/internal/domain/shared"
)

// eventsOf returns one event for the outbox of a test unit of work.
func eventsOf(t *testing.T, a *assignment.Assignment) []shared.Event {
	t.Helper()
	_, events, err := assignment.Queue(input(string(a.ID()), a.OrderID(), a.GroupNo()), candidate("node-a"), hours(2), T0)
	if err != nil {
		t.Fatal(err)
	}
	return events[:1]
}
