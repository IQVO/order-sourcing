package features_test

import (
	"errors"
	"fmt"
	"slices"

	"github.com/cucumber/godog"

	"github.com/claudioed/order-sourcing/internal/application/repository"
	"github.com/claudioed/order-sourcing/internal/application/views"
	"github.com/claudioed/order-sourcing/internal/domain/assignment"
	"github.com/claudioed/order-sourcing/internal/domain/shared"
)

// refusals maps the model's error names to the sentinels the use cases return.
var refusals = map[string]error{
	"ErrStaleAssignmentVersion": assignment.ErrStaleAssignmentVersion,
	"ErrReleaseInFlight":        assignment.ErrReleaseInFlight,
	"ErrReleaseNotPending":      assignment.ErrReleaseNotPending,
	"ErrAssignmentLocked":       assignment.ErrAssignmentLocked,
}

func registerOutcomeSteps(sc *godog.ScenarioContext, w *world) {
	sc.Step(`^the command succeeds$`, func() error {
		if w.lastErr != nil {
			return fmt.Errorf("the command failed: %w", w.lastErr)
		}
		return nil
	})
	sc.Step(`^the command is refused with "([^"]*)"$`, func(name string) error {
		want, ok := refusals[name]
		if !ok {
			return fmt.Errorf("the scenario names an unknown error %q", name)
		}
		if !errors.Is(w.lastErr, want) {
			return fmt.Errorf("the command returned %v, want %s", w.lastErr, name)
		}
		return nil
	})
	sc.Step(`^the command is rejected as invalid$`, func() error {
		if !errors.Is(w.lastErr, shared.ErrValidation) {
			return fmt.Errorf("the command returned %v, want a validation error", w.lastErr)
		}
		return nil
	})
	sc.Step(`^the last command published "([^"]*)"$`, func(names string) error {
		if got := joinNames(w.published()); got != names {
			return fmt.Errorf("published [%s], want [%s]", got, names)
		}
		return nil
	})
	sc.Step(`^the last command published nothing$`, func() error {
		if got := w.published(); len(got) != 0 {
			return fmt.Errorf("published %v, want nothing", got)
		}
		return nil
	})
	registerAssignmentSteps(sc, w)
}

// check loads the assignment of one group and runs an assertion on its view.
func (w *world) check(order string, group int, assert func(v views.Assignment) error) error {
	v, err := w.assignmentOf(order, group)
	if err != nil {
		return err
	}
	return assert(v)
}

func registerAssignmentSteps(sc *godog.ScenarioContext, w *world) {
	sc.Step(`^the assignment of "([^"]*)" group (\d+) is "([^"]*)"$`, func(order string, group int, state string) error {
		return w.check(order, group, func(v views.Assignment) error {
			if v.State != state {
				return fmt.Errorf("state is %s, want %s", v.State, state)
			}
			return nil
		})
	})
	sc.Step(`^the assignment of "([^"]*)" group (\d+) has the candidate node "([^"]*)"$`, func(order string, group int, node string) error {
		return w.check(order, group, func(v views.Assignment) error {
			if v.Candidate == nil || v.Candidate.NodeID != node {
				return fmt.Errorf("candidate is %+v, want node %s", v.Candidate, node)
			}
			return nil
		})
	})
	sc.Step(`^the assignment of "([^"]*)" group (\d+) has a PENDING release number (\d+) to "([^"]*)"$`, func(order string, group, no int, node string) error {
		return w.check(order, group, func(v views.Assignment) error {
			if n := len(v.Releases); n == 0 || v.Releases[n-1].ReleaseNo != no || v.Releases[n-1].NodeID != node || v.Releases[n-1].Outcome != "PENDING" {
				return fmt.Errorf("releases are %+v, want PENDING release %d to %s", v.Releases, no, node)
			}
			return nil
		})
	})
	registerExclusionSteps(sc, w)
}

func registerExclusionSteps(sc *godog.ScenarioContext, w *world) {
	sc.Step(`^the assignment of "([^"]*)" group (\d+) excludes the node "([^"]*)"$`, func(order string, group int, node string) error {
		return w.check(order, group, func(v views.Assignment) error {
			if !slices.ContainsFunc(v.ExcludedNodes, func(e views.ExcludedNode) bool { return e.NodeID == node }) {
				return fmt.Errorf("excluded nodes are %+v, want %s among them", v.ExcludedNodes, node)
			}
			return nil
		})
	})
	sc.Step(`^the assignment of "([^"]*)" group (\d+) excludes no node$`, func(order string, group int) error {
		return w.check(order, group, func(v views.Assignment) error {
			if len(v.ExcludedNodes) != 0 {
				return fmt.Errorf("excluded nodes are %+v, want none", v.ExcludedNodes)
			}
			return nil
		})
	})
	sc.Step(`^the assignment of "([^"]*)" group (\d+) has promise revision (\d+)$`, func(order string, group, revision int) error {
		return w.check(order, group, func(v views.Assignment) error {
			if v.Promise.RevisionNo != revision {
				return fmt.Errorf("promise revision is %d, want %d", v.Promise.RevisionNo, revision)
			}
			return nil
		})
	})
	sc.Step(`^there is exactly (\d+) assignment$`, func(n int) error {
		all, err := w.store.Assignments().List(w.ctx(), repository.AssignmentFilter{})
		if err != nil || len(all) != n {
			return fmt.Errorf("assignments = %d (%v), want %d", len(all), err, n)
		}
		return nil
	})
}
