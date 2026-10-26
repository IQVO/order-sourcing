package usecases_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/claudioed/order-sourcing/internal/application/ports"
	"github.com/claudioed/order-sourcing/internal/application/usecases"
	"github.com/claudioed/order-sourcing/internal/domain/assignment"
	"github.com/claudioed/order-sourcing/internal/domain/shared"
)

// recordedMetrics is a ports.SourcingMetrics that keeps what it was told, as
// "sourced:<STATE>" and "failed:<reason>".
type recordedMetrics struct{ got []string }

func (r *recordedMetrics) GroupSourced(_ context.Context, state string) {
	r.got = append(r.got, "sourced:"+state)
}
func (r *recordedMetrics) SourcingFailed(_ context.Context, reason string) {
	r.got = append(r.got, "failed:"+reason)
}

var _ ports.SourcingMetrics = (*recordedMetrics)(nil)

func queueWithMetrics(e *env, cmd usecases.QueueAssignmentCommand) (*recordedMetrics, error) {
	m := &recordedMetrics{}
	e.deps.Metrics = m
	_, err := usecases.NewQueueAssignment(e.deps).Execute(context.Background(), cmd)
	return m, err
}

func wantMetrics(t *testing.T, m *recordedMetrics, want ...string) {
	t.Helper()
	if !reflect.DeepEqual(m.got, want) {
		t.Fatalf("recorded %v, want %v", m.got, want)
	}
}

// One outcome per created group, by the state the assignment starts in.
func TestQueueAssignmentCountsEachQueuedGroup(t *testing.T) {
	e := newEnv(t)
	m, err := queueWithMetrics(e, placed("evt-1", group(1, "prm-1", assignment.TierNextDay), group(2, "prm-2", assignment.TierStandard)))
	e.must(err)
	wantMetrics(t, m, "sourced:QUEUED", "sourced:QUEUED")
}

func TestQueueAssignmentCountsASameDayGroupAsReleased(t *testing.T) {
	e := newEnv(t)
	rm := e.store.ReadModels()
	node, _ := rm.GetNode(context.Background(), "fc-a")
	node.Subtype = "SUB_SAME_DAY"
	e.must(rm.PutNode(context.Background(), node))
	e.must(rm.PutDeparture(context.Background(), directDeparture()))
	m, err := queueWithMetrics(e, placed("evt-1", group(1, "prm-1", assignment.TierSameDay)))
	e.must(err)
	wantMetrics(t, m, "sourced:RELEASED")
}

func TestQueueAssignmentCountsAGroupWithoutCandidateAsUnsourceable(t *testing.T) {
	e := newEnv(t)
	cmd := placed("evt-1", group(1, "prm-1", assignment.TierNextDay))
	cmd.DestinationPostalPrefix = "99999"
	m, err := queueWithMetrics(e, cmd)
	e.must(err)
	wantMetrics(t, m, "sourced:UNSOURCEABLE")
}

// A replayed event and an already-queued group are not outcomes.
func TestQueueAssignmentCountsNothingForAReplay(t *testing.T) {
	e := newEnv(t)
	_, err := queueWithMetrics(e, placed("evt-1", group(1, "prm-1", assignment.TierNextDay)))
	e.must(err)
	m, err := queueWithMetrics(e, placed("evt-1", group(1, "prm-1", assignment.TierNextDay)))
	e.must(err)
	wantMetrics(t, m)
	m, err = queueWithMetrics(e, placed("evt-2", group(1, "prm-1", assignment.TierNextDay)))
	e.must(err)
	wantMetrics(t, m)
}

// A failed command counts one failure with a stable reason and no group, even
// when an earlier group of the same command had been built.
func TestQueueAssignmentCountsOneFailureWithItsReason(t *testing.T) {
	bad := group(2, "prm-2", assignment.TierNextDay)
	bad.Tier = "WEEKLY"
	cases := []struct {
		name    string
		prepare func(*env)
		cmd     usecases.QueueAssignmentCommand
		want    string
	}{
		{"validation", func(*env) {}, placed("evt-1", group(1, "prm-1", assignment.TierNextDay), bad), "failed:validation-failed"},
		{"infrastructure", func(e *env) { e.outbox.fault = fault{errBoom, 9} }, placed("evt-1", group(1, "prm-1", assignment.TierNextDay)), "failed:internal-error"},
		{"duplicate", func(e *env) { e.assignments.save = fault{ports.ErrDuplicateGroup, 9} }, placed("evt-1", group(1, "prm-1", assignment.TierNextDay)), "failed:duplicate-group"},
		{"conflict", func(e *env) { e.assignments.save = fault{ports.ErrConcurrentUpdate, 2} }, placed("evt-1", group(1, "prm-1", assignment.TierNextDay)), "failed:concurrent-update"},
	}
	for _, c := range cases {
		e := newEnv(t)
		c.prepare(e)
		m, err := queueWithMetrics(e, c.cmd)
		if err == nil {
			t.Fatalf("%s: want an error", c.name)
		}
		if c.name == "validation" {
			wantErr(t, err, shared.ErrValidation)
		}
		if !reflect.DeepEqual(m.got, []string{c.want}) {
			t.Errorf("%s: recorded %v, want [%s]", c.name, m.got, c.want)
		}
	}
}

// A concurrent update that the retry cures is one outcome, not two.
func TestQueueAssignmentCountsARetriedGroupOnce(t *testing.T) {
	e := newEnv(t)
	e.assignments.save = fault{ports.ErrConcurrentUpdate, 1}
	m, err := queueWithMetrics(e, placed("evt-1", group(1, "prm-1", assignment.TierNextDay)))
	e.must(err)
	wantMetrics(t, m, "sourced:QUEUED")
}

// Without Deps.Metrics the use case records nothing and does not panic.
func TestQueueAssignmentWithoutMetricsIsANoOp(t *testing.T) {
	e := newEnv(t)
	if _, err := usecases.NewQueueAssignment(e.deps).Execute(context.Background(), placed("evt-1", group(1, "prm-1", assignment.TierNextDay))); err != nil {
		t.Fatal(err)
	}
}
