package policy_test

import (
	"errors"
	"reflect"
	"testing"

	"github.com/claudioed/order-sourcing/internal/domain/policy"
	"github.com/claudioed/order-sourcing/internal/domain/shared"
)

func TestConsolidationMaxAssignmentsIsOffByDefault(t *testing.T) {
	if policy.Default().ConsolidationMaxAssignments() != 0 {
		t.Fatal("the default policy has consolidation off")
	}
	p, _, err := policy.Publish(0, policy.Input{CrossRegionSurchargeCents: 5}, t0)
	if err != nil || p.ConsolidationMaxAssignments() != 0 {
		t.Fatalf("a document that omits the field switches it off: %v %v", p, err)
	}
}

func TestConsolidationMaxAssignmentsRange(t *testing.T) {
	for n, ok := range map[int]bool{-1: false, 0: true, 1: true, 2: true, 10: true, 11: false} {
		_, _, err := policy.Publish(0, policy.Input{ConsolidationMaxAssignments: n}, t0)
		if ok != (err == nil) || (!ok && !errors.Is(err, shared.ErrValidation)) {
			t.Errorf("consolidationMaxAssignments %d: err = %v, want ok %v", n, err, ok)
		}
	}
}

func TestConsolidationMaxAssignmentsIsPublishedRestoredAndSnapshotted(t *testing.T) {
	p, events, err := policy.Publish(3, policy.Input{ConsolidationMaxAssignments: 4}, t0)
	if err != nil {
		t.Fatal(err)
	}
	if got := events[0].(policy.SourcingPolicyPublished); got.ConsolidationMaxAssignments != 4 || got.Number != 4 {
		t.Fatalf("event = %+v", got)
	}
	back, err := policy.Restore(p.Snapshot())
	if err != nil || !reflect.DeepEqual(back.Snapshot(), p.Snapshot()) || back.ConsolidationMaxAssignments() != 4 {
		t.Fatalf("round trip = %+v %v", back, err)
	}
	s := p.Snapshot()
	s.ConsolidationMaxAssignments = 11
	if _, err := policy.Restore(s); !errors.Is(err, shared.ErrInvalidSnapshot) {
		t.Fatalf("a corrupt row = %v", err)
	}
	s.ConsolidationMaxAssignments = 0 // a row written before ADR 0015
	if back, err := policy.Restore(s); err != nil || back.ConsolidationMaxAssignments() != 0 {
		t.Fatalf("an old row = %v %v", back, err)
	}
}
