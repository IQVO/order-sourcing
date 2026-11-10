package policy_test

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/claudioed/order-sourcing/internal/domain/policy"
	"github.com/claudioed/order-sourcing/internal/domain/shared"
)

var t0 = time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)

func TestDefaultIsVersionZeroWithEveryLeverOff(t *testing.T) {
	d := policy.Default()
	if d.Version() != 0 || d.CrossRegionSurchargeCents() != 0 || d.MaxCostDeviationPercent() != 0 || !d.PublishedAt().IsZero() {
		t.Fatalf("default = %+v", d.Snapshot())
	}
}

func TestPublishCreatesTheNextVersionAndRaisesTheEvent(t *testing.T) {
	p, events, err := policy.Publish(0, policy.Input{CrossRegionSurchargeCents: 250, MaxCostDeviationPercent: 20}, t0.In(time.FixedZone("x", 3600)))
	if err != nil {
		t.Fatal(err)
	}
	if p.Version() != 1 || p.CrossRegionSurchargeCents() != 250 || p.MaxCostDeviationPercent() != 20 || !p.PublishedAt().Equal(t0) || p.PublishedAt().Location() != time.UTC {
		t.Fatalf("policy = %+v", p.Snapshot())
	}
	want := policy.SourcingPolicyPublished{
		Header: shared.Header{Version: 1, At: t0}, Number: 1, CrossRegionSurchargeCents: 250, MaxCostDeviationPercent: 20, PublishedAt: t0,
	}
	if len(events) != 1 || !reflect.DeepEqual(events[0], want) {
		t.Fatalf("events = %+v", events)
	}
	ev := events[0]
	if ev.EventName() != "SourcingPolicyPublished" || ev.Entity() != "policy" || ev.SubjectID() != "policy" || ev.Stream() != shared.StreamAnalytics {
		t.Fatalf("event identity = %s %s %s %s", ev.EventName(), ev.Entity(), ev.SubjectID(), ev.Stream())
	}
}

func TestEveryPublishIsTheNextVersion(t *testing.T) {
	p, _, err := policy.Publish(6, policy.Input{}, t0)
	if err != nil || p.Version() != 7 {
		t.Fatalf("publish after 6 = %v %v", p, err)
	}
	for _, current := range []int{-1, policy.MaxVersion} {
		if _, _, err := policy.Publish(current, policy.Input{}, t0); !errors.Is(err, policy.ErrVersionNotNext) {
			t.Errorf("publish after %d = %v, want ErrVersionNotNext", current, err)
		}
	}
}

func TestPublishValidatesEveryRange(t *testing.T) {
	cases := []struct {
		name string
		in   policy.Input
		ok   bool
	}{
		{"all zero", policy.Input{}, true},
		{"both at the maximum", policy.Input{CrossRegionSurchargeCents: 100000, MaxCostDeviationPercent: 200}, true},
		{"surcharge over", policy.Input{CrossRegionSurchargeCents: 100001}, false},
		{"surcharge negative", policy.Input{CrossRegionSurchargeCents: -1}, false},
		{"deviation over", policy.Input{MaxCostDeviationPercent: 201}, false},
		{"deviation negative", policy.Input{MaxCostDeviationPercent: -1}, false},
	}
	for _, c := range cases {
		_, events, err := policy.Publish(0, c.in, t0)
		if c.ok != (err == nil) || (!c.ok && (!errors.Is(err, shared.ErrValidation) || events != nil)) {
			t.Errorf("%s: err = %v events = %v", c.name, err, events)
		}
	}
}

func TestRestoreRoundTripsAndRefusesACorruptRow(t *testing.T) {
	p, _, err := policy.Publish(2, policy.Input{CrossRegionSurchargeCents: 5, MaxCostDeviationPercent: 7}, t0)
	if err != nil {
		t.Fatal(err)
	}
	back, err := policy.Restore(p.Snapshot())
	if err != nil || !reflect.DeepEqual(back.Snapshot(), p.Snapshot()) {
		t.Fatalf("round trip = %+v %v", back, err)
	}
	good := p.Snapshot()
	bad := []policy.Snapshot{
		{CrossRegionSurchargeCents: 1, MaxCostDeviationPercent: 1, PublishedAt: t0},
		{Version: 1, CrossRegionSurchargeCents: 1, MaxCostDeviationPercent: 1},
		{Version: policy.MaxVersion + 1, PublishedAt: t0},
		{Version: good.Version, CrossRegionSurchargeCents: policy.MaxSurchargeCents + 1, PublishedAt: t0},
		{Version: good.Version, MaxCostDeviationPercent: policy.MaxDeviationPercent + 1, PublishedAt: t0},
	}
	for i, s := range bad {
		if _, err := policy.Restore(s); !errors.Is(err, shared.ErrInvalidSnapshot) {
			t.Errorf("bad snapshot %d: err = %v", i, err)
		}
	}
}
