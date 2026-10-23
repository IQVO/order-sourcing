// Package repository holds the plain query structs the ports use. They live
// here, not in ports, because ports is interfaces only (enforced by
// internal/architecture).
package repository

import (
	"time"

	"github.com/claudioed/order-sourcing/internal/domain/assignment"
)

// DefaultLimit and MaxLimit bound ListAssignments.
const (
	DefaultLimit = 100
	MaxLimit     = 500
)

// AssignmentFilter is the filter of ListAssignments; filters combine with
// AND, a zero field does not filter. NodeID matches the current candidate
// node (QUEUED) or the node of the latest release (RELEASED and later).
type AssignmentFilter struct {
	State   assignment.State
	NodeID  string
	OrderID string
	Limit   int
}

// WorldQuery tells the read side which slice of the read models one search
// needs: the destination's service area and station, the nodes that could
// ship every item, and the departures and waves that are still ahead.
type WorldQuery struct {
	DestinationPostalPrefix string
	CatalogItemIDs          []string
	Now                     time.Time
}
