package usecases

import (
	"context"
	"log/slog"
	"time"

	"github.com/claudioed/order-sourcing/internal/application/views"
	"github.com/claudioed/order-sourcing/internal/domain/assignment"
	"github.com/claudioed/order-sourcing/internal/domain/shared"
)

// memberFn applies the carrier's command to one follower; carrier is the
// aggregate after its own command.
type memberFn func(carrier, follower *assignment.Assignment, now time.Time) ([]shared.Event, error)

// mutateMerged is mutate for a command addressed to the id the vendor side
// knows: the carrier. It applies the command to the carrier and then, in the
// same unit of work, to every follower of its merge, so each member raises its
// own events (ADR 0015). A carrier with no followers behaves exactly as mutate.
func (w writer) mutateMerged(
	ctx context.Context, m Meta, id assignment.ID,
	apply func(ctx context.Context, a *assignment.Assignment, now time.Time) ([]shared.Event, error), member memberFn,
) (views.Assignment, error) {
	return transact(ctx, w.UoW, func(ctx context.Context) (views.Assignment, error) {
		if done, err := w.claim(ctx, m); err != nil || done {
			return views.Assignment{}, err
		}
		a, err := w.Assignments.Get(ctx, id)
		if err != nil {
			return views.Assignment{}, err
		}
		before, now := a.Version(), w.now()
		events, err := apply(ctx, a, now)
		if err != nil {
			return views.Assignment{}, err
		}
		if err := w.persist(ctx, a, before, events); err != nil {
			return views.Assignment{}, err
		}
		for _, fid := range a.Members() {
			f, err := w.Assignments.Get(ctx, fid)
			if err != nil {
				return views.Assignment{}, err
			}
			fbefore := f.Version()
			fevents, err := member(a, f, now)
			if err != nil {
				return views.Assignment{}, err
			}
			if err := w.persist(ctx, f, fbefore, fevents); err != nil {
				return views.Assignment{}, err
			}
		}
		return views.AssignmentFrom(a), nil
	})
}

// dissolve withdraws the merge a RELEASED member belongs to (ADR 0015): the
// carrier raises the withdrawal of the published release, every member's pending
// release becomes WITHDRAWN and every member returns to QUEUED, re-searched with no
// node exclusion under its own (possibly just revised) constraint. trigger is the
// member whose promise revision broke the merged delivery; it is loaded already
// and carries the revision, so it is reused rather than read again.
func (w writer) dissolve(ctx context.Context, trigger *assignment.Assignment, reason string, now time.Time) error {
	carrier := trigger
	if into := trigger.ConsolidatedInto(); into != "" {
		c, err := w.Assignments.Get(ctx, into)
		if err != nil {
			return err
		}
		carrier = c
	}
	members := []*assignment.Assignment{carrier}
	ids := []assignment.ID{carrier.ID()}
	for _, id := range carrier.Members() {
		m := trigger
		if id != trigger.ID() {
			loaded, err := w.Assignments.Get(ctx, id)
			if err != nil {
				return err
			}
			m = loaded
		}
		members, ids = append(members, m), append(ids, id)
	}
	d := assignment.Dissolution{Reason: reason, TriggerID: trigger.ID(), MemberIDs: ids}
	for _, m := range members {
		found, err := w.search(ctx, m, now)
		if err != nil {
			return err
		}
		cand, releaseAt := candidateOf(found.best)
		before := m.Version()
		events, err := m.Requeue(cand, releaseAt, d, now)
		if err != nil {
			return err
		}
		if err := w.persist(ctx, m, before, events); err != nil {
			return err
		}
	}
	slog.InfoContext(ctx, "consolidation dissolved", "carrierAssignmentId", carrier.ID(), "memberAssignmentIds", ids, "reason", reason,
		"triggerAssignmentId", trigger.ID())
	return nil
}
