package usecases

import (
	"context"
	"fmt"
	"time"

	"github.com/claudioed/order-sourcing/internal/application/repository"
	"github.com/claudioed/order-sourcing/internal/domain/assignment"
	"github.com/claudioed/order-sourcing/internal/domain/sourcing"
)

// searchResult is one candidate search: the best option (nil when the
// candidate set is empty), the world it was found in (the release resolves the
// listings from it) and the size of the ranked candidate set.
type searchResult struct {
	best       *sourcing.Option
	world      sourcing.World
	candidates int
	// budgetPreferred is true when the sourcing policy's budget rule put the
	// best option ahead of the pure cheapest one (ADR 0014).
	budgetPreferred bool
	// policy is the sourcing policy this search read (ADR 0014); a release reads
	// the consolidation cap from it, so the merge uses the version the carrier's
	// own search stamped (ADR 0015).
	policy sourcing.Policy
}

// facts is what the search knew about its best option (ADR 0013): the regions
// of the node and of the destination's service area, and the candidate count.
func (s searchResult) facts() assignment.SourcingFacts {
	if s.best == nil {
		return assignment.SourcingFacts{}
	}
	return assignment.SourcingFacts{
		NodeRegionID: s.world.Nodes[s.best.Candidate.NodeID].RegionID, DestinationRegionID: s.world.Area.RegionID,
		CandidateCount: s.candidates,
	}
}

// bestOption loads a fresh snapshot of the read models and ranks the options
// under the constraint (sourcing.Search); best is nil when the candidate set
// is empty.
func (w writer) bestOption(ctx context.Context, now time.Time, prefix string, p assignment.PromiseConstraint,
	lines []assignment.Line, excluded []assignment.ExcludedNode,
) (searchResult, error) {
	items := make([]string, 0, len(lines))
	for _, l := range lines {
		items = append(items, l.CatalogItemID)
	}
	world, err := w.World.Load(ctx, repository.WorldQuery{DestinationPostalPrefix: prefix, CatalogItemIDs: items, Now: now})
	if err != nil {
		return searchResult{}, fmt.Errorf("load read models: %w", err)
	}
	pol, err := w.currentPolicy(ctx)
	if err != nil {
		return searchResult{}, err
	}
	ranking := sourcing.Rank(world, sourcing.Request{
		Now: now, Tier: p.Tier, DeliverByLatest: p.DeliverByLatest, WitnessNodeID: p.WitnessNodeID, Lines: lines, Excluded: excluded,
		Policy: pol,
	})
	if len(ranking.Options) == 0 {
		return searchResult{world: world, policy: pol}, nil
	}
	return searchResult{best: &ranking.Options[0], world: world, candidates: len(ranking.Options), budgetPreferred: ranking.BudgetPreferred, policy: pol}, nil
}

// candidateOf returns the candidate and release point of an option, nil and
// the zero time for none.
func candidateOf(o *sourcing.Option) (*assignment.Candidate, time.Time) {
	if o == nil {
		return nil, time.Time{}
	}
	c := o.Candidate
	return &c, o.ReleaseAt
}

// search is bestOption for an assignment as it stands, with extra exclusions.
func (w writer) search(ctx context.Context, a *assignment.Assignment, now time.Time, extra ...assignment.ExcludedNode) (searchResult, error) {
	excluded := append(a.ExcludedNodes(), extra...)
	return w.bestOption(ctx, now, a.DestinationPostalPrefix(), a.Promise(), a.Lines(), excluded)
}
