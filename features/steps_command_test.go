package features_test

import (
	"strconv"
	"strings"

	"github.com/cucumber/godog"

	"github.com/claudioed/order-sourcing/internal/application/usecases"
	"github.com/claudioed/order-sourcing/internal/domain/assignment"
)

func registerCommandSteps(sc *godog.ScenarioContext, w *world) {
	sc.Step(`^I re-optimise the assignment of "([^"]*)" group (\d+)$`, func(order string, group int) error {
		return w.withID(order, group, func(id assignment.ID) error {
			_, err := usecases.NewReoptimiseAssignment(w.deps).Execute(w.ctx(), id, 0)
			return err
		})
	})
	sc.Step(`^I re-optimise the assignment of "([^"]*)" group (\d+) expecting version (\d+)$`, func(order string, group, version int) error {
		return w.withID(order, group, func(id assignment.ID) error {
			_, err := usecases.NewReoptimiseAssignment(w.deps).Execute(w.ctx(), id, int64(version))
			return err
		})
	})
	sc.Step(`^I release the assignment of "([^"]*)" group (\d+)$`, func(order string, group int) error {
		return w.withID(order, group, func(id assignment.ID) error {
			_, err := usecases.NewReleaseToNode(w.deps).Execute(w.ctx(), id)
			return err
		})
	})
	sc.Step(`^the node (rejects|lets) release (\d+)(?: expire)?$`, w.refuse)
	sc.Step(`^the node accepts release (\d+)$`, func(no int) error {
		return w.onOnly(func(id assignment.ID) error {
			_, err := usecases.NewLockAssignment(w.deps).Execute(w.ctx(), usecases.LockAssignmentCommand{AssignmentID: id, ReleaseNo: no, AcceptedAt: hours(1)})
			return err
		})
	})
	sc.Step(`^the vendor ships the packages "([^"]*)" of "([^"]*)" group (\d+)$`, func(packages, order string, group int) error {
		return w.withID(order, group, func(id assignment.ID) error {
			_, err := usecases.NewMarkShipped(w.deps).Execute(w.ctx(), usecases.MarkShippedCommand{AssignmentID: id, TrackingNumbers: splitList(packages), ShippedAt: hours(2)})
			return err
		})
	})
	sc.Step(`^package "([^"]*)" of "([^"]*)" group (\d+) is delivered$`, func(tracking, order string, group int) error {
		return w.withID(order, group, func(id assignment.ID) error {
			_, err := usecases.NewMarkDelivered(w.deps).Execute(w.ctx(), usecases.MarkDeliveredCommand{AssignmentID: id, TrackingNumber: tracking, DeliveredAt: hours(30)})
			return err
		})
	})
	sc.Step(`^the customer cancels group (\d+) of order "([^"]*)"$`, func(group int, order string) error {
		w.run(func() error {
			_, err := usecases.NewCancelAssignment(w.deps).Execute(w.ctx(), usecases.CancelAssignmentCommand{OrderID: order, GroupNos: []int{group}})
			return err
		})
		return nil
	})
	sc.Step(`^promise "([^"]*)" is revised to revision (\d+) with the deadline in (\d+) hours$`, func(promise string, revision, deadline int) error {
		w.run(func() error {
			_, err := usecases.NewApplyRevisedPromise(w.deps).Execute(w.ctx(), usecases.ApplyRevisedPromiseCommand{PromiseID: promise, RevisionNo: revision, DeliverByLatest: hours(deadline)})
			return err
		})
		return nil
	})
}

// withID runs a command against the assignment of one group.
func (w *world) withID(order string, group int, command func(assignment.ID) error) error {
	id, err := w.idOf(order, group)
	if err != nil {
		return err
	}
	w.run(func() error { return command(id) })
	return nil
}

// onOnly runs a command against the only assignment of the scenario.
func (w *world) onOnly(command func(assignment.ID) error) error {
	return w.withID(w.lastPlaced.OrderID, 1, command)
}

func (w *world) refuse(how, number string) error {
	no, err := strconv.Atoi(number)
	if err != nil {
		return err
	}
	outcome := assignment.OutcomeRejected
	if how == "lets" {
		outcome = assignment.OutcomeExpired
	}
	return w.onOnly(func(id assignment.ID) error {
		_, err := usecases.NewHandleNodeRefusal(w.deps).Execute(w.ctx(), usecases.HandleNodeRefusalCommand{AssignmentID: id, ReleaseNo: no, Outcome: outcome, ReasonCode: "BDD"})
		return err
	})
}

func splitList(s string) []string {
	parts := strings.Split(s, ",")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}
