package usecases

import (
	"context"
	"fmt"
	"time"

	"github.com/claudioed/order-sourcing/internal/application/repository"
	"github.com/claudioed/order-sourcing/internal/domain/assignment"
	"github.com/claudioed/order-sourcing/internal/domain/sourcing"
)

// bestOption loads a fresh snapshot of the read models and returns the best
// option under the constraint, or nil when the candidate set is empty. The
// world is returned for the release, which resolves the listings from it.
func (w writer) bestOption(ctx context.Context, now time.Time, prefix string, p assignment.PromiseConstraint,
	lines []assignment.Line, excluded []assignment.ExcludedNode,
) (*sourcing.Option, sourcing.World, error) {
	items := make([]string, 0, len(lines))
	for _, l := range lines {
		items = append(items, l.CatalogItemID)
	}
	world, err := w.World.Load(ctx, repository.WorldQuery{DestinationPostalPrefix: prefix, CatalogItemIDs: items, Now: now})
	if err != nil {
		return nil, sourcing.World{}, fmt.Errorf("load read models: %w", err)
	}
	opt, ok := sourcing.Best(world, sourcing.Request{
		Now: now, Tier: p.Tier, DeliverByLatest: p.DeliverByLatest, WitnessNodeID: p.WitnessNodeID, Lines: lines, Excluded: excluded,
	})
	if !ok {
		return nil, world, nil
	}
	return &opt, world, nil
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
func (w writer) search(ctx context.Context, a *assignment.Assignment, now time.Time, extra ...assignment.ExcludedNode) (*sourcing.Option, sourcing.World, error) {
	excluded := append(a.ExcludedNodes(), extra...)
	return w.bestOption(ctx, now, a.DestinationPostalPrefix(), a.Promise(), a.Lines(), excluded)
}
