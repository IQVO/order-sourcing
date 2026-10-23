package assignment

import (
	"testing"
	"time"

	"github.com/claudioed/order-sourcing/internal/domain/shared"
)

func TestQueueWithCandidate(t *testing.T) {
	a, events, err := Queue(newInput(), cand("node-a"), at(2), t0)
	wantNoErr(t, err)
	wantState(t, a, StateQueued, 1)
	wantEvents(t, events, "FulfillmentQueued")
	wantEventVersions(t, a, events)
	wantQueuedEvent(t, events[0])
	wantQueuedCandidate(t, a)
	wantQueuedAccessors(t, a)
}

func wantQueuedEvent(t *testing.T, e shared.Event) {
	t.Helper()
	q := e.(FulfillmentQueued)
	if e.Stream() != shared.StreamAnalytics || q.OrderID != "ord-1" || q.GroupNo != 1 || q.PromiseID != "prm-1" ||
		!q.DeliverByLatest.Equal(deadline) || len(q.Lines) != 2 {
		t.Fatalf("queued event = %+v", q)
	}
}

func wantQueuedCandidate(t *testing.T, a *Assignment) {
	t.Helper()
	c, ok := a.Candidate()
	if !ok || c.NodeID != "node-a" || !a.ReleaseAt().Equal(at(2)) || !a.QueuedAt().Equal(t0) || !a.UpdatedAt().Equal(t0) {
		t.Fatalf("candidate %+v releaseAt %v", c, a.ReleaseAt())
	}
}

func wantQueuedAccessors(t *testing.T, a *Assignment) {
	t.Helper()
	if a.OrderID() != "ord-1" || a.GroupNo() != 1 || a.DestinationPostalPrefix() != "30301" || a.Promise().PromiseID != "prm-1" ||
		len(a.Lines()) != 2 || len(a.Releases()) != 0 || len(a.ExcludedNodes()) != 0 || a.ID() != "asg-1" {
		t.Fatal("accessors do not reflect the input")
	}
}

func TestQueueWithoutCandidateIsUnsourceableAtOnce(t *testing.T) {
	a, events, err := Queue(newInput(), nil, time.Time{}, t0)
	wantNoErr(t, err)
	wantState(t, a, StateUnsourceable, 1)
	wantEvents(t, events, "FulfillmentQueued", "FulfillmentUnsourceable")
	wantEventVersions(t, a, events)
	u := events[1].(FulfillmentUnsourceable)
	if u.Reason != ReasonNoCandidate || u.PromiseID != "prm-1" || u.OrderID != "ord-1" || u.GroupNo != 1 || len(u.ExcludedNodes) != 0 {
		t.Fatalf("unsourceable event = %+v", u)
	}
	if events[1].Stream() != shared.StreamEvents {
		t.Fatal("FulfillmentUnsourceable goes to the events stream")
	}
	if _, ok := a.Candidate(); ok {
		t.Fatal("no candidate expected")
	}
}

func TestQueueRejectsInvalidInput(t *testing.T) {
	mut := func(f func(*New)) New { in := newInput(); f(&in); return in }
	cases := map[string]New{
		"bad id":          mut(func(n *New) { n.ID = "" }),
		"bad order":       mut(func(n *New) { n.OrderID = "-x" }),
		"group zero":      mut(func(n *New) { n.GroupNo = 0 }),
		"group too big":   mut(func(n *New) { n.GroupNo = MaxInt32 + 1 }),
		"no prefix":       mut(func(n *New) { n.DestinationPostalPrefix = "" }),
		"prefix too long": mut(func(n *New) { n.DestinationPostalPrefix = "12345678901234567" }),
		"bad promise id":  mut(func(n *New) { n.Promise.PromiseID = "" }),
		"bad tier":        mut(func(n *New) { n.Promise.Tier = "WEEKLY" }),
		"no deadline":     mut(func(n *New) { n.Promise.DeliverByLatest = time.Time{} }),
		"bad witness":     mut(func(n *New) { n.Promise.WitnessNodeID = "-n" }),
		"negative rev":    mut(func(n *New) { n.Promise.RevisionNo = -1 }),
		"revision big":    mut(func(n *New) { n.Promise.RevisionNo = MaxInt32 + 1 }),
		"no lines":        mut(func(n *New) { n.Lines = nil }),
		"too many lines": mut(func(n *New) {
			n.Lines = make([]Line, MaxLines+1)
			for i := range n.Lines {
				n.Lines[i] = Line{CatalogItemID: "c", Quantity: 1}
			}
		}),
		"bad item":     mut(func(n *New) { n.Lines[0].CatalogItemID = "" }),
		"bad listing":  mut(func(n *New) { n.Lines[0].ListingID = "-l" }),
		"zero qty":     mut(func(n *New) { n.Lines[0].Quantity = 0 }),
		"qty too big":  mut(func(n *New) { n.Lines[0].Quantity = MaxInt32 + 1 }),
		"line no neg":  mut(func(n *New) { n.Lines[0].LineNo = -1 }),
		"line no huge": mut(func(n *New) { n.Lines[0].LineNo = MaxInt32 + 1 }),
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			_, _, err := Queue(in, cand("node-a"), at(2), t0)
			wantErr(t, err, shared.ErrValidation)
		})
	}
}

func TestQueueAcceptsTheBoundaries(t *testing.T) {
	in := newInput()
	in.GroupNo = MaxInt32
	in.Promise.RevisionNo = MaxInt32
	in.DestinationPostalPrefix = "1234567890123456"
	in.Lines = []Line{{LineNo: 0, CatalogItemID: "c", Quantity: MaxInt32}}
	if _, _, err := Queue(in, cand("node-a"), at(2), t0); err != nil {
		t.Fatalf("boundaries must be accepted: %v", err)
	}
	in.Lines = []Line{{LineNo: MaxInt32, CatalogItemID: "c", Quantity: 1}}
	if _, _, err := Queue(in, cand("node-a"), at(2), t0); err != nil {
		t.Fatalf("the highest lineNo must be accepted: %v", err)
	}
	in.Lines = make([]Line, MaxLines)
	for i := range in.Lines {
		in.Lines[i] = Line{CatalogItemID: "c", Quantity: 1}
	}
	if _, _, err := Queue(in, cand("node-a"), at(2), t0); err != nil {
		t.Fatalf("MaxLines must be accepted: %v", err)
	}
}

func TestCandidateInvariants(t *testing.T) {
	mut := func(f func(*Candidate)) *Candidate { c := cand("node-a"); f(c); return c }
	validation := map[string]*Candidate{
		"bad node":   mut(func(c *Candidate) { c.NodeID = "" }),
		"bad pickup": mut(func(c *Candidate) { c.PickupDepartureID = "" }),
		"bad wave":   mut(func(c *Candidate) { c.DeliveryWaveID = "" }),
		"no eta":     mut(func(c *Candidate) { c.ETA = time.Time{} }),
		"no cutoff":  mut(func(c *Candidate) { c.PickupCutoffAt = time.Time{} }),
		"too many legs": mut(func(c *Candidate) {
			c.Legs = make([]string, MaxLegs+1)
			for i := range c.Legs {
				c.Legs[i] = "l"
			}
		}),
		"bad leg":       mut(func(c *Candidate) { c.Legs = []string{""} }),
		"negative cost": mut(func(c *Candidate) { c.Cost = -1 }),
	}
	for name, c := range validation {
		t.Run(name, func(t *testing.T) {
			_, _, err := Queue(newInput(), c, at(2), t0)
			wantErr(t, err, shared.ErrValidation)
		})
	}
	late := mut(func(c *Candidate) { c.ETA = deadline.Add(time.Second) })
	_, _, err := Queue(newInput(), late, at(2), t0)
	wantErr(t, err, ErrCandidateBreaksPromise)
	onTime := mut(func(c *Candidate) {
		c.ETA = deadline
		c.Legs = make([]string, MaxLegs)
		for i := range c.Legs {
			c.Legs[i] = "l"
		}
	})
	if _, _, err := Queue(newInput(), onTime, at(2), t0); err != nil {
		t.Fatalf("eta equal to deliverByLatest and MaxLegs legs are allowed: %v", err)
	}
	free := mut(func(c *Candidate) { c.Cost = 0 })
	if _, _, err := Queue(newInput(), free, at(2), t0); err != nil {
		t.Fatalf("a zero cost is not negative: %v", err)
	}
	if _, _, err := Queue(newInput(), cand("node-a"), time.Time{}, t0); err == nil {
		t.Fatal("a candidate needs a releaseAt")
	}
}
