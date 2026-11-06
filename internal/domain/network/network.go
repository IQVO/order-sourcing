// Package network holds the plain value types of the five local read models
// (NodeProfileView, NetworkGraphView, AvailabilityView, ScorecardView,
// PromiseBookPressureView) and the pure rules that merge a sibling event into
// a row: the newer revision, version or instant wins, an older one is ignored,
// and events may arrive in any order (ADR 0003). Nothing here is an aggregate:
// no invariant of this context lives in these rows.
package network

import "time"

// Node types and statuses used by the search.
const (
	TypeFulfillmentCenter = "FULFILLMENT_CENTER"
	OperatorVendor        = "VENDOR"
	StatusProposed        = "PROPOSED"
	StatusActive          = "ACTIVE"
	StatusSuspended       = "SUSPENDED"
	StatusRetired         = "RETIRED"
	SubtypeSubSameDay     = "SUB_SAME_DAY"
	SubtypeMicro          = "MICRO"
	Window30d             = "30d"
)

// Node is a row of NodeProfileView.
type Node struct {
	ID               string
	Type             string
	Subtype          string
	Operator         string
	RegionID         string
	Status           string
	StatusAt         time.Time
	ProfileRevision  int
	HandlingMinutes  int
	SortDwellMinutes int
}

// Registered is the data of NodeRegistered that the view keeps.
type Registered struct {
	Type, Subtype, Operator, RegionID string
	At                                time.Time
}

// ApplyRegistered stores the static facts of the node. A registration never
// moves the status: it sets PROPOSED only when no status event was seen.
func (n *Node) ApplyRegistered(r Registered) {
	n.Type, n.Subtype, n.Operator, n.RegionID = r.Type, r.Subtype, r.Operator, r.RegionID
	if n.Status == "" {
		n.Status, n.StatusAt = StatusProposed, r.At
	}
}

// ApplyProfile stores a declared profile when its revision is above the stored one.
func (n *Node) ApplyProfile(revision, handlingMinutes, sortDwellMinutes int) bool {
	if revision <= n.ProfileRevision {
		return false
	}
	n.ProfileRevision, n.HandlingMinutes, n.SortDwellMinutes = revision, handlingMinutes, sortDwellMinutes
	return true
}

// ApplyStatus stores ACTIVE, SUSPENDED or RETIRED when at is not older than
// the stored status instant. RETIRED is final.
func (n *Node) ApplyStatus(status string, at time.Time) bool {
	if n.Status == StatusRetired || at.Before(n.StatusAt) || (n.Status == status && n.StatusAt.Equal(at)) {
		return false
	}
	n.Status, n.StatusAt = status, at
	return true
}

// Eligible reports whether the node can take customer demand: an ACTIVE
// vendor fulfilment centre with a declared profile.
func (n Node) Eligible() bool {
	return n.Type == TypeFulfillmentCenter && n.Operator == OperatorVendor && n.Status == StatusActive && n.ProfileRevision > 0
}

// Lane is a row of NetworkGraphView. CostPerUnitCents is the planner's
// declared marginal cost of moving ONE package-unit across the lane
// (LaneCostDeclared); CostDeclaredAt zero means no declaration was seen, so
// the lane contributes 0 to a money-denominated estimate and marks it
// incomplete. A declaration may arrive before LaneOpened: the row is stored
// keyed by laneId anyway and LaneOpened later completes it without losing the
// cost (ADR 0003, ADR 0011).
type Lane struct {
	ID               string
	Origin           string
	Destination      string
	Mode             string
	Closed           bool
	CostPerUnitCents int64
	CostDeclaredAt   time.Time
}

// ApplyOpened stores the static facts of an opened lane, keeping the closed
// flag and any declared cost (a cost that arrived first is never lost).
func (l *Lane) ApplyOpened(o Lane) {
	closed, cost, at := l.Closed, l.CostPerUnitCents, l.CostDeclaredAt
	*l = o
	l.Closed, l.CostPerUnitCents, l.CostDeclaredAt = closed, cost, at
}

// ApplyCostDeclared stores a declared per-unit cost unless its instant is
// older than the stored one; 0 is a legal declared cost (ADR 0011).
func (l *Lane) ApplyCostDeclared(costPerUnitCents int64, at time.Time) bool {
	if at.Before(l.CostDeclaredAt) {
		return false
	}
	l.CostPerUnitCents, l.CostDeclaredAt = costPerUnitCents, at
	return true
}

// CostDeclared reports whether a cost declaration was seen for the lane.
func (l Lane) CostDeclared() bool { return !l.CostDeclaredAt.IsZero() }

// Departure is a row of NetworkGraphView.
type Departure struct {
	ID             string
	LaneID         string
	Kind           string
	Origin         string
	Destination    string
	CutoffAt       time.Time
	DepartsAt      time.Time
	ArrivesAt      time.Time
	Capacity       int
	Remaining      int
	RemainingKnown bool
	Scheduled      bool
	Cancelled      bool
}

// ApplySchedule stores the schedule. The remaining capacity is the capacity
// unless a capacity event already set it.
func (d *Departure) ApplySchedule(s Departure) {
	rem, known := d.Remaining, d.RemainingKnown
	cancelled := d.Cancelled
	*d = s
	d.Scheduled, d.Cancelled = true, cancelled
	if known {
		d.Remaining, d.RemainingKnown = rem, true
		return
	}
	d.Remaining, d.RemainingKnown = s.Capacity, true
}

// ApplyCapacity stores the remaining packages.
func (d *Departure) ApplyCapacity(remaining int) {
	d.Remaining, d.RemainingKnown = remaining, true
}

// Usable reports whether a package can be booked on the departure now.
func (d Departure) Usable() bool { return d.Scheduled && !d.Cancelled && d.Remaining > 0 }

// Wave is a row of NetworkGraphView. CostPerStopCents is the planner's
// declared marginal cost of ONE delivery stop on the wave
// (WaveCostDeclared); CostDeclaredAt zero means no declaration was seen, so
// the wave contributes 0 to a money-denominated estimate and marks it
// incomplete. A declaration may arrive before DeliveryWaveScheduled: the row
// is stored keyed by waveId anyway and the schedule later completes it without
// losing the cost (ADR 0003, ADR 0011).
type Wave struct {
	ID               string
	StationID        string
	InductCutoffAt   time.Time
	WindowStart      time.Time
	WindowEnd        time.Time
	Capacity         int
	Remaining        int
	RemainingKnown   bool
	Scheduled        bool
	Cancelled        bool
	CostPerStopCents int64
	CostDeclaredAt   time.Time
}

// ApplySchedule stores the schedule, keeping remaining stops a capacity event
// set and any declared cost (a cost that arrived first is never lost).
func (w *Wave) ApplySchedule(s Wave) {
	rem, known := w.Remaining, w.RemainingKnown
	cancelled := w.Cancelled
	cost, at := w.CostPerStopCents, w.CostDeclaredAt
	*w = s
	w.Scheduled, w.Cancelled = true, cancelled
	w.CostPerStopCents, w.CostDeclaredAt = cost, at
	if known {
		w.Remaining, w.RemainingKnown = rem, true
		return
	}
	w.Remaining, w.RemainingKnown = s.Capacity, true
}

// ApplyCapacity stores the remaining stops.
func (w *Wave) ApplyCapacity(remaining int) {
	w.Remaining, w.RemainingKnown = remaining, true
}

// ApplyCostDeclared stores a declared per-stop cost unless its instant is
// older than the stored one; 0 is a legal declared cost (ADR 0011).
func (w *Wave) ApplyCostDeclared(costPerStopCents int64, at time.Time) bool {
	if at.Before(w.CostDeclaredAt) {
		return false
	}
	w.CostPerStopCents, w.CostDeclaredAt = costPerStopCents, at
	return true
}

// CostDeclared reports whether a cost declaration was seen for the wave.
func (w Wave) CostDeclared() bool { return !w.CostDeclaredAt.IsZero() }

// Usable reports whether a stop can be assigned to the wave now.
func (w Wave) Usable() bool { return w.Scheduled && !w.Cancelled && w.Remaining > 0 }

// ServiceArea is a row of NetworkGraphView: the station and region of a postal prefix.
type ServiceArea struct {
	PostalPrefix string
	StationID    string
	RegionID     string
	Version      int
}

// Newer reports whether s replaces the stored row o.
func (s ServiceArea) Newer(o ServiceArea) bool { return s.Version > o.Version }

// Region is a row of NetworkGraphView.
type Region struct {
	ID   string
	Code string
	Name string
}

// Listing is a row of AvailabilityView.
type Listing struct {
	ID            string
	CatalogItemID string
	NodeID        string
	AvailableQty  int
	AsOf          time.Time
	Suppressed    bool
}

// ApplyAvailability stores a quantity unless asOf is older than the stored one.
func (l *Listing) ApplyAvailability(qty int, asOf time.Time) bool {
	if asOf.Before(l.AsOf) {
		return false
	}
	l.AvailableQty, l.AsOf = qty, asOf
	return true
}

// CanShip reports whether the listing covers quantity in full.
func (l Listing) CanShip(quantity int) bool { return !l.Suppressed && l.AvailableQty >= quantity }

// Scorecard is a row of ScorecardView.
type Scorecard struct {
	NodeID     string
	Window     string
	Grade      string
	ComputedAt time.Time
}

// Newer reports whether s replaces the stored row o.
func (s Scorecard) Newer(o Scorecard) bool { return !s.ComputedAt.Before(o.ComputedAt) }

// Pressure is a row of PromiseBookPressureView: an exhausted promise book.
type Pressure struct {
	BookID            string
	NodeID            string
	PickupDepartureID string
}
