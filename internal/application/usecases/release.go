package usecases

import (
	"context"
	"log/slog"
	"maps"
	"slices"
	"time"

	"github.com/claudioed/order-sourcing/internal/application/repository"
	"github.com/claudioed/order-sourcing/internal/application/views"
	"github.com/claudioed/order-sourcing/internal/domain/assignment"
	"github.com/claudioed/order-sourcing/internal/domain/sourcing"
)

// ReleaseToNode releases a QUEUED assignment to its best node, together with
// the QUEUED assignments that merge into the release (ADR 0015).
type ReleaseToNode struct{ writer }

// NewReleaseToNode builds the use case.
func NewReleaseToNode(d Deps) *ReleaseToNode { return &ReleaseToNode{writer{d}} }

// releaseOutcome is what a committed release did, for the metrics and the log
// that must not fire for a rolled-back attempt.
type releaseOutcome struct {
	preferred bool
	carrier   assignment.ID
	members   []assignment.ID
	node      string
}

// Execute re-checks the candidate against fresh read models and releases to
// the best node, or declares the group UNSOURCEABLE when none is left. An
// assignment that is no longer QUEUED is left alone (a second timer firing).
// The merge loads several aggregates, so the whole release is one unit of work
// whose optimistic version checks make two concurrent releases safe.
func (uc *ReleaseToNode) Execute(ctx context.Context, id assignment.ID) (views.Assignment, error) {
	var out releaseOutcome
	v, err := transact(ctx, uc.UoW, func(ctx context.Context) (views.Assignment, error) {
		out = releaseOutcome{}
		a, err := uc.Assignments.Get(ctx, id)
		if err != nil {
			return views.Assignment{}, err
		}
		if a.State() != assignment.StateQueued {
			return views.AssignmentFrom(a), nil
		}
		now := uc.now()
		found, err := uc.search(ctx, a, now)
		if err != nil {
			return views.Assignment{}, err
		}
		if err := uc.release(ctx, a, found, now, &out); err != nil {
			return views.Assignment{}, err
		}
		return views.AssignmentFrom(a), nil
	})
	if err != nil {
		return v, err
	}
	// After the commit, so a rolled-back attempt is not counted (ADR 0014, ADR 0015).
	if out.preferred {
		uc.policyMetrics().BudgetPreferred(ctx, budgetKindRegion)
	}
	if len(out.members) > 1 {
		uc.consolidationMetrics().Consolidated(ctx, len(out.members)-1)
		slog.InfoContext(ctx, "consolidated release", "carrierAssignmentId", out.carrier, "memberAssignmentIds", out.members, "nodeId", out.node)
	}
	return v, nil
}

// release persists the release of a: merged with the followers Merge finds, or
// alone as before.
func (uc *ReleaseToNode) release(ctx context.Context, a *assignment.Assignment, found searchResult, now time.Time, out *releaseOutcome) error {
	before := a.Version()
	if found.best == nil {
		events, err := a.Release(nil, nil, assignment.SourcingFacts{}, now)
		if err != nil {
			return err
		}
		return uc.persist(ctx, a, before, events)
	}
	out.preferred = found.budgetPreferred
	merged, world, ok, err := uc.merge(ctx, a, found, now)
	if err != nil {
		return err
	}
	if ok {
		return uc.releaseMerged(ctx, a, merged, searchResult{best: &merged.Option, world: world, candidates: merged.Candidates}, now, out)
	}
	// The search only returns nodes that ship every line, so the listings resolve; a missing
	// listing would fail the aggregate's own check.
	lines, _ := sourcing.ResolveLines(found.world, found.best.Candidate.NodeID, a.Lines())
	events, err := a.Release(&found.best.Candidate, lines, found.facts(), now)
	if err != nil {
		return err
	}
	return uc.persist(ctx, a, before, events)
}

// merge looks for followers when the policy is on and the carrier is a keyed
// CONSOLIDATED group: it reads the QUEUED assignments of the same key, loads the
// world once for the union of their catalogue items and asks sourcing.Merge. The
// world it returns is the one the merged option was found in.
func (uc *ReleaseToNode) merge(ctx context.Context, a *assignment.Assignment, found searchResult, now time.Time) (sourcing.Merged, sourcing.World, bool, error) {
	pol := found.policy
	if !pol.ConsolidationOn() || a.ConsolidationKey() == "" || a.Promise().Tier != assignment.TierConsolidated {
		return sourcing.Merged{}, sourcing.World{}, false, nil
	}
	cands, err := uc.Assignments.QueuedByKey(ctx, a.ConsolidationKey(), a.ID(), 2*pol.ConsolidationMaxAssignments)
	if err != nil {
		return sourcing.Merged{}, sourcing.World{}, false, err
	}
	cands = slices.DeleteFunc(cands, func(c *assignment.Assignment) bool { return c.DestinationPostalPrefix() != a.DestinationPostalPrefix() })
	if len(cands) == 0 {
		return sourcing.Merged{}, sourcing.World{}, false, nil
	}
	items := map[string]bool{}
	for _, m := range append([]*assignment.Assignment{a}, cands...) {
		for _, l := range m.Lines() {
			items[l.CatalogItemID] = true
		}
	}
	world, err := uc.World.Load(ctx, repository.WorldQuery{DestinationPostalPrefix: a.DestinationPostalPrefix(), CatalogItemIDs: slices.Sorted(maps.Keys(items)), Now: now})
	if err != nil {
		return sourcing.Merged{}, sourcing.World{}, false, err
	}
	merged, ok := sourcing.Merge(sourcing.MergeInput{World: world, Now: now, Policy: pol, Carrier: a, Solo: *found.best, Candidates: cands})
	return merged, world, ok, nil
}

// releaseMerged releases the carrier with its followers at the merged option
// and saves every member in the unit of work.
func (uc *ReleaseToNode) releaseMerged(ctx context.Context, a *assignment.Assignment, m sourcing.Merged, found searchResult, now time.Time, out *releaseOutcome) error {
	cand := &m.Option.Candidate
	members := make([]assignment.MergedMember, 0, len(m.Followers))
	for _, f := range m.Followers {
		p := f.Promise()
		members = append(members, assignment.MergedMember{ID: f.ID(), OrderID: f.OrderID(), GroupNo: f.GroupNo(), PromiseID: p.PromiseID,
			DeliverByLatest: p.DeliverByLatest, Lines: m.Lines[f.ID()]})
	}
	before := a.Version()
	events, err := a.ReleaseConsolidated(cand, m.Lines[a.ID()], members, found.facts(), now)
	if err != nil {
		return err
	}
	if err := uc.persist(ctx, a, before, events); err != nil {
		return err
	}
	out.carrier, out.node, out.members = a.ID(), cand.NodeID, []assignment.ID{a.ID()}
	for _, f := range m.Followers {
		fbefore := f.Version()
		fevents, err := f.ReleaseAsFollower(a.ID(), cand, m.Lines[f.ID()], len(members)+1, found.facts(), now)
		if err != nil {
			return err
		}
		if err := uc.persist(ctx, f, fbefore, fevents); err != nil {
			return err
		}
		out.members = append(out.members, f.ID())
	}
	return nil
}
