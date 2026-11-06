// Package sourcing is the pure candidate search of order-sourcing: given a
// snapshot of the local read models (World) and what an assignment must keep
// (Request), it finds the nodes and paths that still meet deliverByLatest,
// prices them with the AssignmentCost policy and sets the VirtualQueue release
// point. No I/O: the clock is a parameter and the World is a value (ADR 0002,
// A11 to A16).
package sourcing

import (
	"cmp"
	"slices"
	"strings"
	"time"

	"github.com/claudioed/order-sourcing/internal/domain/assignment"
	"github.com/claudioed/order-sourcing/internal/domain/network"
)

// The AssignmentCost and VirtualQueue constants (ADR 0002, A12, A13, A15, A16;
// ADR 0011 demotes the ordinal terms to the legacy tie-break score).
const (
	LegCost             int64 = 100
	HourCost            int64 = 10
	CrossRegionPenalty  int64 = 1000
	BookPressurePenalty int64 = 500
	AckBudget                 = 30 * time.Minute
	// MaxDepartures bounds a path: the pickup plus at most four onward departures.
	MaxDepartures = 5
)

// gradePenalty is the scorecardPenalty of a graded node; PROVISIONAL and a
// node without scorecard add nothing (G20).
var gradePenalty = map[string]int64{"A": 0, "B": 50, "C": 200, "D": 500}

// World is the snapshot of the read models one search reads.
type World struct {
	Nodes        map[string]network.Node
	Departures   []network.Departure
	Lanes        map[string]network.Lane
	Waves        []network.Wave
	Area         network.ServiceArea
	Listings     []network.Listing
	Discontinued map[string]bool
	Scorecards   map[string]network.Scorecard
	Pressure     []network.Pressure
}

// Request is what the search must satisfy.
type Request struct {
	Now             time.Time
	Tier            assignment.Tier
	DeliverByLatest time.Time
	WitnessNodeID   string
	Lines           []assignment.Line
	Excluded        []assignment.ExcludedNode
}

// Option is one ranked candidate with its release point.
type Option struct {
	Candidate assignment.Candidate
	ReleaseAt time.Time
}

// Search returns the options ordered best first: the lowest estimated cost in
// cents, then the lowest legacy penalty score, then the witness node, then the
// lower node id, then the lower pickup departure id (ADR 0011).
func Search(w World, r Request) []Option {
	if w.Area.StationID == "" {
		return nil
	}
	s := searcher{w: w, r: r, byOrigin: indexByOrigin(w.Departures)}
	var out []Option
	for _, id := range sortedKeys(w.Nodes) {
		out = append(out, s.node(w.Nodes[id])...)
	}
	slices.SortFunc(out, func(a, b Option) int {
		return cmp.Or(
			cmp.Compare(a.Candidate.EstimatedCostCents, b.Candidate.EstimatedCostCents),
			cmp.Compare(a.Candidate.Cost, b.Candidate.Cost),
			boolRank(a.Candidate.NodeID == r.WitnessNodeID, b.Candidate.NodeID == r.WitnessNodeID),
			strings.Compare(a.Candidate.NodeID, b.Candidate.NodeID),
			strings.Compare(a.Candidate.PickupDepartureID, b.Candidate.PickupDepartureID),
		)
	})
	return out
}

// Best returns the best option, or false when the candidate set is empty.
func Best(w World, r Request) (Option, bool) {
	opts := Search(w, r)
	if len(opts) == 0 {
		return Option{}, false
	}
	return opts[0], true
}

// ResolveLines returns the lines with the listingId of the node, or false when
// the node cannot ship every line in full.
func ResolveLines(w World, nodeID string, lines []assignment.Line) ([]assignment.Line, bool) {
	out := slices.Clone(lines)
	for i, l := range out {
		if w.Discontinued[l.CatalogItemID] {
			return nil, false
		}
		idx := slices.IndexFunc(w.Listings, func(x network.Listing) bool { return x.NodeID == nodeID && x.CatalogItemID == l.CatalogItemID })
		if idx < 0 || !w.Listings[idx].CanShip(l.Quantity) {
			return nil, false
		}
		out[i].ListingID = w.Listings[idx].ID
	}
	return out, true
}

func boolRank(a, b bool) int { // true sorts first
	if a == b {
		return 0
	}
	if a {
		return -1
	}
	return 1
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

func indexByOrigin(deps []network.Departure) map[string][]network.Departure {
	out := map[string][]network.Departure{}
	for _, d := range deps {
		out[d.Origin] = append(out[d.Origin], d)
	}
	return out
}

type searcher struct {
	w        World
	r        Request
	byOrigin map[string][]network.Departure
}

// path is the best way from a pickup to a delivery wave found so far. cost is
// the legacy ordinal score of the path's legs; est is the money-denominated
// estimate of the onward legs plus the final wave (the pickup's lane is added
// by option, which knows the pickup departure).
type path struct {
	legs []string
	wave network.Wave
	cost int64
	est  estimate
}

// estimate is the money-denominated cost of a path in planner-declared cents:
// the sum of the declared per-unit lane costs of every leg and the declared
// per-stop cost of the final wave. An undeclared component contributes 0 and
// counts towards unknown, which marks the whole estimate incomplete (ADR 0011).
// The comparator never looks at unknown: an incomplete estimate still competes
// by its sum, 0s included.
type estimate struct {
	cents   int64
	unknown int
}

func (e estimate) add(cents int64, declared bool) estimate {
	if !declared {
		e.unknown++
		return e
	}
	e.cents += cents
	return e
}

func (s searcher) node(n network.Node) []Option {
	if !n.Eligible() || s.isExcluded(n.ID) {
		return nil
	}
	if s.r.Tier == assignment.TierSameDay && n.Subtype != network.SubtypeSubSameDay && n.Subtype != network.SubtypeMicro {
		return nil
	}
	if _, ok := ResolveLines(s.w, n.ID, s.r.Lines); !ok {
		return nil
	}
	ready := s.r.Now.Add(time.Duration(n.HandlingMinutes) * time.Minute)
	var out []Option
	for _, d := range s.byOrigin[n.ID] {
		if !s.usable(d) || d.CutoffAt.Before(ready) {
			continue
		}
		p, ok := s.explore(d, map[string]bool{n.ID: true}, nil, 1)
		if !ok {
			continue
		}
		out = append(out, s.option(n, d, p))
	}
	return out
}

func (s searcher) isExcluded(node string) bool {
	return slices.ContainsFunc(s.r.Excluded, func(e assignment.ExcludedNode) bool { return e.NodeID == node })
}

func (s searcher) usable(d network.Departure) bool {
	if !d.Usable() {
		return false
	}
	l, known := s.w.Lanes[d.LaneID]
	return !known || !l.Closed
}

// laneCost is the declared per-unit cost of a departure's lane: 0 and false
// when the view does not know the lane or no cost was declared for it.
func (s searcher) laneCost(d network.Departure) (int64, bool) {
	l, known := s.w.Lanes[d.LaneID]
	if !known || !l.CostDeclared() {
		return 0, false
	}
	return l.CostPerUnitCents, true
}

// waveCost is the declared per-stop cost of a wave: 0 and false when no cost
// was declared for it.
func waveCost(w network.Wave) (int64, bool) {
	if !w.CostDeclared() {
		return 0, false
	}
	return w.CostPerStopCents, true
}

func (s searcher) option(n network.Node, pickup network.Departure, p path) Option {
	// The money estimate covers every departure traversed — the pickup's lane
	// plus the onward legs — and the final wave's per-stop cost. Per package-unit:
	// the World and the request carry no package count, so nothing multiplies
	// the sum (ADR 0011).
	est := p.est.add(s.laneCost(pickup))
	cost := p.cost + HourCost*int64(max(0, int(p.wave.WindowStart.Sub(pickup.DepartsAt)/time.Hour))) + s.penalties(n, pickup)
	releaseAt := s.r.Now
	if s.r.Tier != assignment.TierSameDay {
		releaseAt = pickup.CutoffAt.Add(-time.Duration(n.HandlingMinutes)*time.Minute - AckBudget)
		if releaseAt.Before(s.r.Now) {
			releaseAt = s.r.Now
		}
	}
	return Option{
		Candidate: assignment.Candidate{
			NodeID: n.ID, PickupDepartureID: pickup.ID, PickupCutoffAt: pickup.CutoffAt, Legs: slices.Clone(p.legs),
			DeliveryWaveID: p.wave.ID, ETA: p.wave.WindowEnd, Cost: cost,
			EstimatedCostCents: est.cents, CostComplete: est.unknown == 0,
		},
		ReleaseAt: releaseAt,
	}
}

func (s searcher) penalties(n network.Node, pickup network.Departure) int64 {
	var total int64
	if n.RegionID != "" && s.w.Area.RegionID != "" && n.RegionID != s.w.Area.RegionID {
		total += CrossRegionPenalty
	}
	if sc, ok := s.w.Scorecards[n.ID]; ok && sc.Window == network.Window30d {
		total += gradePenalty[sc.Grade]
	}
	if slices.ContainsFunc(s.w.Pressure, func(p network.Pressure) bool { return p.NodeID == n.ID && p.PickupDepartureID == pickup.ID }) {
		total += BookPressurePenalty
	}
	return total
}

// explore walks onward from departure d (the depth-th of the path) and returns
// the cheapest way to a wave of the destination's station that meets
// deliverByLatest. legs are the departure ids after the pickup so far.
func (s searcher) explore(d network.Departure, visited map[string]bool, legs []string, depth int) (path, bool) {
	if d.Destination == s.w.Area.StationID {
		return s.finish(d, legs)
	}
	if depth >= MaxDepartures || s.r.Tier == assignment.TierSameDay || visited[d.Destination] {
		return path{}, false
	}
	ready := d.ArrivesAt.Add(time.Duration(s.w.Nodes[d.Destination].SortDwellMinutes) * time.Minute)
	visited[d.Destination] = true
	defer delete(visited, d.Destination)
	var best path
	found := false
	for _, next := range s.byOrigin[d.Destination] {
		if !s.usable(next) || next.CutoffAt.Before(ready) {
			continue
		}
		p, ok := s.explore(next, visited, append(slices.Clone(legs), next.ID), depth+1)
		if !ok {
			continue
		}
		p.est = p.est.add(s.laneCost(next))
		if !found || better(p, best) {
			best, found = p, true
		}
	}
	return best, found
}

// finish picks the earliest wave the last departure can feed.
func (s searcher) finish(d network.Departure, legs []string) (path, bool) {
	var best network.Wave
	found := false
	for _, w := range s.w.Waves {
		if w.StationID != s.w.Area.StationID || !w.Usable() || w.InductCutoffAt.Before(d.ArrivesAt) || w.WindowEnd.After(s.r.DeliverByLatest) {
			continue
		}
		if !found || w.WindowEnd.Before(best.WindowEnd) || (w.WindowEnd.Equal(best.WindowEnd) && w.ID < best.ID) {
			best, found = w, true
		}
	}
	if !found {
		return path{}, false
	}
	cents, declared := waveCost(best)
	return path{legs: slices.Clone(legs), wave: best, cost: LegCost * int64(len(legs)), est: estimate{}.add(cents, declared)}, true
}

// better orders two paths: the lower money estimate first, then the lower
// legacy leg score, then the earlier wave, then the lexicographically lower
// legs, so the search is deterministic (ADR 0011).
func better(a, b path) bool {
	return cmp.Or(
		cmp.Compare(a.est.cents, b.est.cents),
		cmp.Compare(a.cost, b.cost),
		a.wave.WindowEnd.Compare(b.wave.WindowEnd),
		slices.Compare(a.legs, b.legs),
	) < 0
}
