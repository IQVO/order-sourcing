package usecases

import (
	"context"
	"time"

	"github.com/claudioed/order-sourcing/internal/application/views"
	"github.com/claudioed/order-sourcing/internal/domain/assignment"
	"github.com/claudioed/order-sourcing/internal/domain/sourcing"
)

// GroupCommand is one shipment group of a placed order.
type GroupCommand struct {
	GroupNo         int
	PromiseID       string
	Tier            assignment.Tier
	WitnessNodeID   string
	DeliverByLatest time.Time
	Lines           []assignment.Line
}

// QueueAssignmentCommand is the input of QueueAssignment (CustomerOrderPlaced).
type QueueAssignmentCommand struct {
	Meta
	OrderID                 string
	DestinationPostalPrefix string
	Groups                  []GroupCommand
}

// QueueAssignment creates one assignment per group.
type QueueAssignment struct{ writer }

// NewQueueAssignment builds the use case.
func NewQueueAssignment(d Deps) *QueueAssignment { return &QueueAssignment{writer{d}} }

// Execute queues every group not queued yet (a repeated order is a no-op, ADR
// 0002 A1). A SAME_DAY group is released at once; a group with no candidate is
// UNSOURCEABLE at once (A4). All groups commit in one transaction.
func (uc *QueueAssignment) Execute(ctx context.Context, cmd QueueAssignmentCommand) ([]views.Assignment, error) {
	out, err := uc.execute(ctx, cmd)
	uc.recordOutcome(ctx, out, err)
	return out, err
}

func (uc *QueueAssignment) execute(ctx context.Context, cmd QueueAssignmentCommand) ([]views.Assignment, error) {
	return transact(ctx, uc.UoW, func(ctx context.Context) ([]views.Assignment, error) {
		if done, err := uc.claim(ctx, cmd.Meta); err != nil || done {
			return nil, err
		}
		out := make([]views.Assignment, 0, len(cmd.Groups))
		for _, g := range cmd.Groups {
			a, err := uc.queueGroup(ctx, cmd, g)
			if err != nil {
				return nil, err
			}
			if a != nil {
				out = append(out, views.AssignmentFrom(a))
			}
		}
		return out, nil
	})
}

func (uc *QueueAssignment) queueGroup(ctx context.Context, cmd QueueAssignmentCommand, g GroupCommand) (*assignment.Assignment, error) {
	_, err := uc.Assignments.FindByGroup(ctx, cmd.OrderID, g.GroupNo)
	exists, err := found(err)
	if err != nil || exists {
		return nil, err
	}
	now := uc.now()
	promise := assignment.PromiseConstraint{PromiseID: g.PromiseID, Tier: g.Tier, DeliverByLatest: g.DeliverByLatest, WitnessNodeID: g.WitnessNodeID}
	best, world, err := uc.bestOption(ctx, now, cmd.DestinationPostalPrefix, promise, g.Lines, nil)
	if err != nil {
		return nil, err
	}
	cand, releaseAt := candidateOf(best)
	a, events, err := assignment.Queue(assignment.New{
		ID: uc.IDs.NewAssignmentID(), OrderID: cmd.OrderID, GroupNo: g.GroupNo, DestinationPostalPrefix: cmd.DestinationPostalPrefix,
		Promise: promise, Lines: g.Lines,
	}, cand, releaseAt, now)
	if err != nil {
		return nil, err
	}
	if err := uc.Assignments.Save(ctx, a); err != nil {
		return nil, err
	}
	if err := uc.publish(ctx, events); err != nil {
		return nil, err
	}
	if best != nil && g.Tier == assignment.TierSameDay {
		return a, uc.releaseNow(ctx, a, best, world, now)
	}
	return a, nil
}

// releaseNow releases an assignment to the option's node on the world the
// option was found in.
func (uc *QueueAssignment) releaseNow(ctx context.Context, a *assignment.Assignment, best *sourcing.Option, world sourcing.World, now time.Time) error {
	// The search only returns nodes that ship every line (see ReleaseToNode).
	lines, _ := sourcing.ResolveLines(world, best.Candidate.NodeID, a.Lines())
	before := a.Version()
	events, err := a.Release(&best.Candidate, lines, now)
	if err != nil {
		return err
	}
	return uc.persist(ctx, a, before, events)
}
