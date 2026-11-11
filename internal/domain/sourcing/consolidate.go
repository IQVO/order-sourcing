package sourcing

import (
	"slices"
	"time"

	"github.com/claudioed/order-sourcing/internal/domain/assignment"
	"github.com/claudioed/order-sourcing/internal/domain/network"
)

// MergeInput is what Merge reads (ADR 0015). The World is loaded ONCE for the
// union of the catalogue items of the carrier and every candidate, Policy is
// read once, Solo is the carrier's own best option and Candidates are the
// QUEUED assignments that share the carrier's consolidationKey, ordered by
// deliverByLatest then id.
type MergeInput struct {
	World      World
	Now        time.Time
	Policy     Policy
	Carrier    *assignment.Assignment
	Solo       Option
	Candidates []*assignment.Assignment
}

// Merged is the outcome of a successful merge: the merged option (always at the
// carrier's node), the followers that joined in order, the lines of every
// member (the carrier included) resolved on the summed stock, and the size of
// the ranked candidate set the merged option was chosen from.
type Merged struct {
	Option     Option
	Followers  []*assignment.Assignment
	Lines      map[assignment.ID][]assignment.Line
	Candidates int
}

// Merge decides which candidates join the carrier's release. It is pure and
// deterministic. A candidate joins greedily, in order, only if rules c1 to c5
// of ADR 0015 hold: identity (same non-empty key and destination prefix, both
// tiers CONSOLIDATED), node (the carrier's solo node is not excluded and the
// merged search returns an option there), overlap (the merged ETA lies inside
// the members' promised windows), stock (one covering owner per item on the
// summed quantity) and caps (member count and total lines). It returns false
// when nobody joins: the carrier then releases alone.
func Merge(in MergeInput) (Merged, bool) {
	limit := in.Policy.ConsolidationMaxAssignments
	if !in.Policy.ConsolidationOn() || in.Carrier.ConsolidationKey() == "" || in.Carrier.Promise().Tier != assignment.TierConsolidated {
		return Merged{}, false
	}
	members := []*assignment.Assignment{in.Carrier}
	var out Merged
	for i, c := range in.Candidates {
		if len(members) >= limit || i >= 2*limit {
			break
		}
		if !in.eligible(c) {
			continue
		}
		trial := append(slices.Clone(members), c)
		if totalLines(trial) > assignment.MaxLines {
			continue
		}
		opt, count, ok := in.mergedOption(trial)
		if !ok {
			continue
		}
		lines, ok := ResolveMerged(in.World, in.Solo.Candidate.NodeID, trial)
		if !ok {
			continue
		}
		members = trial
		out = Merged{Option: opt, Lines: lines, Candidates: count}
	}
	if len(members) < 2 {
		return Merged{}, false
	}
	out.Followers = members[1:]
	return out, true
}

// eligible is rules c1 and the node half of c2: the candidate is a QUEUED
// CONSOLIDATED assignment of the same key and destination, and does not exclude
// the carrier's node.
func (in MergeInput) eligible(c *assignment.Assignment) bool {
	cr := in.Carrier
	if c.ID() == cr.ID() || c.State() != assignment.StateQueued || c.ConsolidationKey() != cr.ConsolidationKey() ||
		c.DestinationPostalPrefix() != cr.DestinationPostalPrefix() || c.Promise().Tier != assignment.TierConsolidated {
		return false
	}
	node := in.Solo.Candidate.NodeID
	return !slices.ContainsFunc(c.ExcludedNodes(), func(e assignment.ExcludedNode) bool { return e.NodeID == node })
}

// mergedOption runs the merged search for the trial member set and returns the
// first option at the carrier's node (rule c2) with the ranked set size. The
// request's window is the intersection of the members' windows, so the ETA of
// the option lies inside it (rule c3); the summed lines make the node ship the
// whole union (the stock half of rule c4).
func (in MergeInput) mergedOption(trial []*assignment.Assignment) (Option, int, bool) {
	carrier := in.Carrier.Promise()
	req := Request{
		Now: in.Now, Tier: carrier.Tier, DeliverByLatest: carrier.DeliverByLatest, NotBefore: carrier.DeliverByEarliest,
		WitnessNodeID: carrier.WitnessNodeID, Lines: SumLines(trial), Policy: in.Policy,
	}
	for _, m := range trial[1:] {
		p := m.Promise()
		if p.DeliverByLatest.Before(req.DeliverByLatest) {
			req.DeliverByLatest = p.DeliverByLatest
		}
		if p.DeliverByEarliest.After(req.NotBefore) {
			req.NotBefore = p.DeliverByEarliest
		}
	}
	for _, m := range trial {
		for _, e := range m.ExcludedNodes() {
			if !slices.ContainsFunc(req.Excluded, func(o assignment.ExcludedNode) bool { return o.NodeID == e.NodeID }) {
				req.Excluded = append(req.Excluded, e)
			}
		}
	}
	ranking := Rank(in.World, req)
	node := in.Solo.Candidate.NodeID
	i := slices.IndexFunc(ranking.Options, func(o Option) bool { return o.Candidate.NodeID == node })
	if i < 0 {
		return Option{}, 0, false
	}
	return ranking.Options[i], len(ranking.Options), true
}

func totalLines(members []*assignment.Assignment) int {
	n := 0
	for _, m := range members {
		n += len(m.Lines())
	}
	return n
}

// SumLines groups the members' lines by catalogue item and sums the
// quantities: one line per item, ordered by item id, no line number.
func SumLines(members []*assignment.Assignment) []assignment.Line {
	sums := map[string]int{}
	for _, m := range members {
		for _, l := range m.Lines() {
			sums[l.CatalogItemID] += l.Quantity
		}
	}
	out := make([]assignment.Line, 0, len(sums))
	for _, item := range sortedKeys(sums) {
		out = append(out, assignment.Line{CatalogItemID: item, Quantity: sums[item]})
	}
	return out
}

// ResolveMerged is rule c4: at the node, the summed quantity of every catalogue
// item must be covered by ONE owner (OwnerChoice, ADR 0012), and every member
// line of that item takes that listing and owner. Owners may differ between
// items. It returns the resolved lines of every member, or false when some item
// cannot be covered or is discontinued.
func ResolveMerged(w World, nodeID string, members []*assignment.Assignment) (map[assignment.ID][]assignment.Line, bool) {
	chosen := map[string]network.Listing{}
	for _, sum := range SumLines(members) {
		if w.Discontinued[sum.CatalogItemID] {
			return nil, false
		}
		l, ok := OwnerChoice(sum.Quantity, listingsAt(w.Listings, nodeID, sum.CatalogItemID))
		if !ok {
			return nil, false
		}
		chosen[sum.CatalogItemID] = l
	}
	out := make(map[assignment.ID][]assignment.Line, len(members))
	for _, m := range members {
		lines := m.Lines()
		for i := range lines {
			l := chosen[lines[i].CatalogItemID]
			lines[i].ListingID, lines[i].OwnerRef = l.ID, l.OwnerRef
		}
		out[m.ID()] = lines
	}
	return out, true
}
