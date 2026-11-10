// Package sourcing is the pure candidate search of order-sourcing: given a
// snapshot of the local read models (World) and what an assignment must keep
// (Request), it finds the nodes and paths that still meet deliverByLatest,
// prices them with the AssignmentCost policy and sets the VirtualQueue release
// point. No I/O: the clock is a parameter and the World is a value (ADR 0002,
// A11 to A16).
package sourcing

import (
	"slices"
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

// Request is what the search must satisfy. Policy is the sourcing policy the
// caller read once for this search (ADR 0014); its zero value is the default
// policy, which leaves the ordering exactly as ADR 0011 defines it.
type Request struct {
	Now             time.Time
	Tier            assignment.Tier
	DeliverByLatest time.Time
	WitnessNodeID   string
	Lines           []assignment.Line
	Excluded        []assignment.ExcludedNode
	Policy          Policy
}

// Option is one ranked candidate with its release point. CrossRegion is true
// when the node's region and the delivery area's region are both known and
// differ (the condition of the legacy penalty and of the policy surcharge).
type Option struct {
	Candidate   assignment.Candidate
	ReleaseAt   time.Time
	CrossRegion bool
}

// Search returns the options ordered best first (see Rank).
func Search(w World, r Request) []Option { return Rank(w, r).Options }

// Best returns the best option, or false when the candidate set is empty.
func Best(w World, r Request) (Option, bool) {
	opts := Search(w, r)
	if len(opts) == 0 {
		return Option{}, false
	}
	return opts[0], true
}

// ResolveLines returns the lines with the listingId and ownerRef the
// OwnerChoice rule picks at the node, or false when the node cannot ship every
// line in full from ONE owner per line (ADR 0012).
func ResolveLines(w World, nodeID string, lines []assignment.Line) ([]assignment.Line, bool) {
	out := slices.Clone(lines)
	for i, l := range out {
		if w.Discontinued[l.CatalogItemID] {
			return nil, false
		}
		chosen, ok := OwnerChoice(l.Quantity, listingsAt(w.Listings, nodeID, l.CatalogItemID))
		if !ok {
			return nil, false
		}
		out[i].ListingID, out[i].OwnerRef = chosen.ID, chosen.OwnerRef
	}
	return out, true
}

// listingsAt returns the listings of one catalogue item at one node, every owner.
func listingsAt(all []network.Listing, nodeID, catalogItemID string) []network.Listing {
	var out []network.Listing
	for _, x := range all {
		if x.NodeID == nodeID && x.CatalogItemID == catalogItemID {
			out = append(out, x)
		}
	}
	return out
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
