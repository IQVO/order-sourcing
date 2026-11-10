package repocontract

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/claudioed/order-sourcing/internal/application/ports"
	"github.com/claudioed/order-sourcing/internal/domain/policy"
)

func runPolicies(t *testing.T, f Factory) {
	t.Run("a miss is ErrNotFound", func(t *testing.T) { policyMiss(t, f(t)) })
	t.Run("save and current round trip every field", func(t *testing.T) { policyRoundTrip(t, f(t)) })
	t.Run("current is the highest version and every version is kept", func(t *testing.T) { policyVersions(t, f(t)) })
	t.Run("a version that exists is ErrConcurrentUpdate", func(t *testing.T) { policyDuplicate(t, f(t)) })
	t.Run("a rolled back unit stores no version", func(t *testing.T) { policyRollback(t, f(t)) })
}

// published builds version current+1 with the given levers.
func published(t *testing.T, current, surcharge, percent int) *policy.Policy {
	t.Helper()
	return publishedMerge(t, current, surcharge, percent, 0)
}

// publishedMerge is published with the consolidation cap of ADR 0015.
func publishedMerge(t *testing.T, current, surcharge, percent, merge int) *policy.Policy {
	t.Helper()
	p, _, err := policy.Publish(current, policy.Input{CrossRegionSurchargeCents: surcharge, MaxCostDeviationPercent: percent, ConsolidationMaxAssignments: merge}, hours(current+1))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func savePolicy(t *testing.T, fx Fixture, p *policy.Policy) {
	t.Helper()
	if err := fx.Policies.Save(ctx(), p); err != nil {
		t.Fatalf("Save policy v%d: %v", p.Version(), err)
	}
}

func policyMiss(t *testing.T, fx Fixture) {
	if _, err := fx.Policies.Current(ctx()); !errors.Is(err, ports.ErrNotFound) {
		t.Fatalf("Current of an empty store = %v", err)
	}
}

func policyRoundTrip(t *testing.T, fx Fixture) {
	p := publishedMerge(t, 0, 250, 20, 4)
	savePolicy(t, fx, p)
	got, err := fx.Policies.Current(ctx())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Snapshot(), p.Snapshot()) {
		t.Fatalf("round trip differs:\n got %+v\nwant %+v", got.Snapshot(), p.Snapshot())
	}
	if s := got.Snapshot(); s.Version != 1 || s.CrossRegionSurchargeCents != 250 || s.MaxCostDeviationPercent != 20 || s.ConsolidationMaxAssignments != 4 || !s.PublishedAt.Equal(hours(1)) {
		t.Fatalf("snapshot = %+v", s)
	}
}

func policyVersions(t *testing.T, fx Fixture) {
	savePolicy(t, fx, published(t, 0, 100, 10))
	savePolicy(t, fx, published(t, 1, 0, 0))
	savePolicy(t, fx, published(t, 2, 100000, 200))
	got, err := fx.Policies.Current(ctx())
	if err != nil || got.Version() != 3 || got.CrossRegionSurchargeCents() != 100000 || got.MaxCostDeviationPercent() != 200 {
		t.Fatalf("Current = %+v %v", got, err)
	}
	// Version 1 and 2 are kept: saving them again conflicts.
	for _, v := range []int{0, 1} {
		if err := fx.Policies.Save(ctx(), published(t, v, 1, 1)); !errors.Is(err, ports.ErrConcurrentUpdate) {
			t.Fatalf("saving version %d again = %v", v+1, err)
		}
	}
}

func policyDuplicate(t *testing.T, fx Fixture) {
	savePolicy(t, fx, published(t, 0, 5, 5))
	if err := fx.Policies.Save(ctx(), published(t, 0, 9, 9)); !errors.Is(err, ports.ErrConcurrentUpdate) {
		t.Fatalf("a second version 1 = %v", err)
	}
	got, err := fx.Policies.Current(ctx())
	if err != nil || got.CrossRegionSurchargeCents() != 5 {
		t.Fatalf("the first version-1 row must stay: %+v %v", got, err)
	}
}

func policyRollback(t *testing.T, fx Fixture) {
	boom := errors.New("boom")
	err := fx.UoW.Do(ctx(), func(c context.Context) error {
		if err := fx.Policies.Save(c, published(t, 0, 5, 5)); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("Do = %v", err)
	}
	if _, err := fx.Policies.Current(ctx()); !errors.Is(err, ports.ErrNotFound) {
		t.Fatalf("the rolled back version is visible: %v", err)
	}
}
