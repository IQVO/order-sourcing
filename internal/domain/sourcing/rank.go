package sourcing

import (
	"cmp"
	"slices"
	"strings"
)

// Policy is the sourcing policy as the pure search reads it (ADR 0014): the
// use case reads the current version once per search and passes it in. The zero
// value is the default policy (version 0, every lever off).
type Policy struct {
	// Version is the policy version read; 0 when none was published.
	Version int
	// CrossRegionSurchargeCents is added to the effective cost of a candidate
	// whose node region differs from the delivery area region (both known).
	CrossRegionSurchargeCents int64
	// MaxCostDeviationPercent is the regional budget over the cheapest
	// effective cost; 0 switches the budget rule off.
	MaxCostDeviationPercent int
	// ConsolidationMaxAssignments is the most assignments one merged release
	// holds; 0 and 1 switch consolidation off (ADR 0015).
	ConsolidationMaxAssignments int
}

// ConsolidationOn reports whether releases may merge.
func (p Policy) ConsolidationOn() bool { return p.ConsolidationMaxAssignments >= 2 }

// Ranking is the ordered candidate set of one search. BudgetPreferred is true
// when the budget rule put a different option first than the pure cheapest
// ordering would have (the ordersourcing_budget_preferred_total counter).
type Ranking struct {
	Options         []Option
	BudgetPreferred bool
}

// Rank returns the options ordered best first. The order is: the budget tier
// (only when maxCostDeviationPercent > 0: within the budget and in-region, then
// within the budget and cross-region, then over the budget), the lowest
// effective cost in cents, then the lowest legacy penalty score, then the
// witness node, then the lower node id, then the lower pickup departure id
// (ADR 0011, ADR 0014). With the default policy the order is that of ADR 0011.
func Rank(w World, r Request) Ranking {
	if w.Area.StationID == "" {
		return Ranking{}
	}
	s := searcher{w: w, r: r, byOrigin: indexByOrigin(w.Departures)}
	var out []Option
	for _, id := range sortedKeys(w.Nodes) {
		out = append(out, s.node(w.Nodes[id])...)
	}
	if !r.Policy.budgetOn() || len(out) < 2 {
		slices.SortFunc(out, orderBy(r, func(Option) int { return 0 }))
		return Ranking{Options: out}
	}
	pure := slices.Clone(out)
	slices.SortFunc(pure, orderBy(r, func(Option) int { return 0 }))
	slices.SortFunc(out, orderBy(r, r.Policy.tier(pure[0].Candidate.EffectiveCostCents())))
	return Ranking{Options: out, BudgetPreferred: !sameOption(out[0], pure[0])}
}

func (p Policy) budgetOn() bool { return p.MaxCostDeviationPercent > 0 }

// tier ranks an option against the budget of the given cheapest effective cost:
// 0 within the budget and in-region, 1 within the budget and cross-region, 2
// over the budget. The budget is cheapest + floor(cheapest * percent / 100).
func (p Policy) tier(cheapest int64) func(Option) int {
	budget := cheapest + cheapest*int64(p.MaxCostDeviationPercent)/100
	return func(o Option) int {
		switch {
		case o.Candidate.EffectiveCostCents() > budget:
			return 2
		case o.CrossRegion:
			return 1
		default:
			return 0
		}
	}
}

// orderBy is the comparator: the budget tier, then the money, then (only for a
// CONSOLIDATED group, ADR 0016) the later pickup cutoff, then the ADR 0011
// tie-breaks.
func orderBy(r Request, tier func(Option) int) func(a, b Option) int {
	return func(a, b Option) int {
		return cmp.Or(
			cmp.Compare(tier(a), tier(b)),
			cmp.Compare(a.Candidate.EffectiveCostCents(), b.Candidate.EffectiveCostCents()),
			holdOrder(r, a, b),
			cmp.Compare(a.Candidate.Cost, b.Candidate.Cost),
			boolRank(a.Candidate.NodeID == r.WitnessNodeID, b.Candidate.NodeID == r.WitnessNodeID),
			strings.Compare(a.Candidate.NodeID, b.Candidate.NodeID),
			strings.Compare(a.Candidate.PickupDepartureID, b.Candidate.PickupDepartureID),
		)
	}
}

// holdOrder prefers the later pickup cutoff when the request holds the latest
// pickup; with it off every pair compares equal, so the ordering is unchanged.
func holdOrder(r Request, a, b Option) int {
	if !r.HoldLatestPickup {
		return 0
	}
	return b.Candidate.PickupCutoffAt.Compare(a.Candidate.PickupCutoffAt)
}

// sameOption reports whether two options are the same node and pickup (the
// ordering's last tie-breaks make that pair unique).
func sameOption(a, b Option) bool {
	return a.Candidate.NodeID == b.Candidate.NodeID && a.Candidate.PickupDepartureID == b.Candidate.PickupDepartureID
}
