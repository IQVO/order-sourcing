package sourcing

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/claudioed/order-sourcing/internal/domain/assignment"
	"github.com/claudioed/order-sourcing/internal/domain/network"
)

var testKey = strings.Repeat("cd", 32)

// spec describes one QUEUED assignment of a merge test.
type spec struct {
	id       string
	tier     assignment.Tier
	key      string
	prefix   string
	earliest time.Time
	latest   time.Time
	lines    []assignment.Line
	excluded []string
	state    assignment.State
}

func member(id string, qty int) spec {
	return spec{
		id: id, tier: assignment.TierConsolidated, key: testKey, prefix: "30301", earliest: h(20), latest: h(48),
		lines: []assignment.Line{{LineNo: 1, CatalogItemID: "cat-1", Quantity: qty}}, state: assignment.StateQueued,
	}
}

func (s spec) build(t *testing.T) *assignment.Assignment {
	t.Helper()
	snap := assignment.Snapshot{
		ID: assignment.ID(s.id), OrderID: "ord-" + s.id, GroupNo: 1, DestinationPostalPrefix: s.prefix,
		Promise: assignment.PromiseConstraint{PromiseID: "prm-" + s.id, Tier: s.tier, DeliverByEarliest: s.earliest, DeliverByLatest: s.latest, WitnessNodeID: "fc-a"},
		Lines:   s.lines, State: s.state, Version: 1, QueuedAt: h(-5), UpdatedAt: h(-5), ConsolidationKey: s.key,
	}
	if s.state == assignment.StateQueued {
		node, dep, eta := "fc-a", "pk-a", h(26)
		if slices.Contains(s.excluded, node) {
			node, dep = "fc-b", "pk-b"
		}
		if s.latest.Before(eta) {
			eta = s.latest
		}
		snap.Candidate = &assignment.Candidate{NodeID: node, PickupDepartureID: dep, PickupCutoffAt: h(4), DeliveryWaveID: "w1", ETA: eta}
		snap.ReleaseAt = h(3)
	}
	for _, n := range s.excluded {
		snap.ExcludedNodes = append(snap.ExcludedNodes, assignment.ExcludedNode{NodeID: n, Reason: "REJECTED"})
	}
	a, err := assignment.Restore(snap)
	if err != nil {
		t.Fatalf("restore %s: %v", s.id, err)
	}
	return a
}

// mergeWorld is world() with a second, later wave and plenty of stock.
func mergeWorld() World {
	w := world()
	w.Waves = append(w.Waves, wave("w2", 40, 44, 50))
	w.Listings = []network.Listing{listing("fc-a", "cat-1", 10), listing("fc-b", "cat-1", 10)}
	return w
}

func policy(n int) Policy { return Policy{Version: 3, ConsolidationMaxAssignments: n} }

// runMerge merges candidates into carrier on w. It reports the result of Merge.
func runMerge(t *testing.T, w World, p Policy, carrier spec, cands ...spec) (Merged, bool) {
	t.Helper()
	c := carrier.build(t)
	// The carrier's solo option is the one ReleaseToNode computes today: a plain search.
	req := Request{Now: now, Tier: assignment.TierTwoDay, DeliverByLatest: c.Promise().DeliverByLatest, WitnessNodeID: "fc-a", Lines: c.Lines(), Policy: p}
	solo, ok := Best(w, req)
	if !ok {
		t.Fatal("the carrier has no solo option")
	}
	in := MergeInput{World: w, Now: now, Policy: p, Carrier: c, Solo: solo}
	for _, s := range cands {
		in.Candidates = append(in.Candidates, s.build(t))
	}
	return Merge(in)
}

func wantFollowers(t *testing.T, m Merged, ok bool, want ...string) {
	t.Helper()
	got := make([]string, 0, len(m.Followers))
	for _, f := range m.Followers {
		got = append(got, string(f.ID()))
	}
	if ok != (len(want) > 0) || strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("followers = %v (merged %v), want %v", got, ok, want)
	}
}

func TestMergeJoinsACompatibleCandidate(t *testing.T) {
	m, ok := runMerge(t, mergeWorld(), policy(5), member("asg-1", 3), member("asg-2", 4))
	wantFollowers(t, m, ok, "asg-2")
	if m.Option.Candidate.NodeID != "fc-a" || m.Option.Candidate.PolicyVersion != 3 || m.Candidates < 1 {
		t.Fatalf("option = %+v from %d", m.Option.Candidate, m.Candidates)
	}
	for id, lines := range m.Lines {
		if len(lines) != 1 || lines[0].ListingID != "lst-cat-1-fc-a" {
			t.Fatalf("lines of %s = %+v", id, lines)
		}
	}
	again, _ := runMerge(t, mergeWorld(), policy(5), member("asg-1", 3), member("asg-2", 4))
	if !again.Option.Candidate.Same(m.Option.Candidate) || again.Candidates != m.Candidates {
		t.Fatal("Merge must be deterministic")
	}
}

func TestMergeRefusesEveryTierButConsolidated(t *testing.T) {
	for _, tier := range []assignment.Tier{assignment.TierSameDay, assignment.TierNextDay, assignment.TierTwoDay, assignment.TierStandard} {
		t.Run("candidate "+string(tier), func(t *testing.T) {
			c := member("asg-2", 1)
			c.tier = tier
			m, ok := runMerge(t, mergeWorld(), policy(5), member("asg-1", 1), c)
			wantFollowers(t, m, ok)
		})
		t.Run("carrier "+string(tier), func(t *testing.T) {
			carrier := member("asg-1", 1)
			carrier.tier = tier
			m, ok := runMerge(t, mergeWorld(), policy(5), carrier, member("asg-2", 1))
			wantFollowers(t, m, ok)
		})
	}
}

func TestMergeIdentityRule(t *testing.T) {
	other := strings.Repeat("ef", 32)
	cases := map[string]func(s *spec){
		"another key":    func(s *spec) { s.key = other },
		"no key":         func(s *spec) { s.key = "" },
		"another prefix": func(s *spec) { s.prefix = "30302" },
		"not queued":     func(s *spec) { s.state = assignment.StateUnsourceable },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			c := member("asg-2", 1)
			mutate(&c)
			m, ok := runMerge(t, mergeWorld(), policy(5), member("asg-1", 1), c)
			wantFollowers(t, m, ok)
		})
	}
	t.Run("the carrier itself", func(t *testing.T) {
		m, ok := runMerge(t, mergeWorld(), policy(5), member("asg-1", 1), member("asg-1", 1))
		wantFollowers(t, m, ok)
	})
	t.Run("a carrier with no key", func(t *testing.T) {
		carrier := member("asg-1", 1)
		carrier.key = ""
		m, ok := runMerge(t, mergeWorld(), policy(5), carrier, member("asg-2", 1))
		wantFollowers(t, m, ok)
	})
}

func TestMergeNodeRule(t *testing.T) {
	t.Run("a candidate that excluded the carrier node", func(t *testing.T) {
		c := member("asg-2", 1)
		c.excluded = []string{"fc-a"}
		m, ok := runMerge(t, mergeWorld(), policy(5), member("asg-1", 1), c)
		wantFollowers(t, m, ok)
	})
	t.Run("a candidate that excluded another node", func(t *testing.T) {
		c := member("asg-2", 1)
		c.excluded = []string{"fc-b"}
		m, ok := runMerge(t, mergeWorld(), policy(5), member("asg-1", 1), c)
		wantFollowers(t, m, ok, "asg-2")
		if m.Option.Candidate.NodeID != "fc-a" {
			t.Fatalf("the merge moved the carrier to %s", m.Option.Candidate.NodeID)
		}
	})
	t.Run("the merged search finds no option at the node", func(t *testing.T) {
		w := mergeWorld()
		w.Listings = []network.Listing{listing("fc-a", "cat-1", 2), listing("fc-b", "cat-1", 10)}
		m, ok := runMerge(t, w, policy(5), member("asg-1", 1), member("asg-2", 2))
		wantFollowers(t, m, ok) // 3 units: only fc-b could ship them, the carrier stays at fc-a
	})
}

func TestMergeOverlapBoundaries(t *testing.T) {
	cases := map[string]struct {
		carrierFrom, candFrom, candTo time.Time
		want                          bool
	}{
		"eta exactly at the earliest":  {h(26), h(20), h(48), true},
		"earliest one hour after eta":  {h(27), h(20), h(48), false},
		"eta exactly at the latest":    {h(20), h(20), h(26), true},
		"latest one hour before eta":   {h(20), h(20), h(25), false},
		"candidate window starts late": {h(20), h(27), h(48), false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			carrier := member("asg-1", 1)
			carrier.earliest = tc.carrierFrom
			c := member("asg-2", 1)
			c.earliest, c.latest = tc.candFrom, tc.candTo
			w := mergeWorld()
			w.Waves = w.Waves[:1]
			m, ok := runMerge(t, w, policy(5), carrier, c)
			if ok != tc.want {
				t.Fatalf("merged = %v, want %v (%+v)", ok, tc.want, m.Option.Candidate)
			}
		})
	}
}

func TestMergeUsesALaterWaveInsideEveryWindow(t *testing.T) {
	carrier := member("asg-1", 1)
	carrier.latest = h(60)
	c := member("asg-2", 1)
	c.earliest, c.latest = h(40), h(60)
	m, ok := runMerge(t, mergeWorld(), policy(5), carrier, c)
	wantFollowers(t, m, ok, "asg-2")
	if m.Option.Candidate.DeliveryWaveID != "w2" || !m.Option.Candidate.ETA.Equal(h(50)) {
		t.Fatalf("merged wave = %s eta %v, want w2 at h(50)", m.Option.Candidate.DeliveryWaveID, m.Option.Candidate.ETA)
	}
	c.latest = h(49)
	m, ok = runMerge(t, mergeWorld(), policy(5), carrier, c)
	wantFollowers(t, m, ok)
}

func TestMergeStockRule(t *testing.T) {
	t.Run("summed stock exactly covered", func(t *testing.T) {
		m, ok := runMerge(t, mergeWorld(), policy(5), member("asg-1", 6), member("asg-2", 4))
		wantFollowers(t, m, ok, "asg-2")
	})
	t.Run("summed stock one unit short", func(t *testing.T) {
		w := mergeWorld()
		w.Listings = []network.Listing{listing("fc-a", "cat-1", 10), listing("fc-b", "cat-1", 11)}
		m, ok := runMerge(t, w, policy(5), member("asg-1", 6), member("asg-2", 5))
		wantFollowers(t, m, ok)
	})
	t.Run("no single owner covers the sum", func(t *testing.T) {
		w := mergeWorld()
		w.Listings = []network.Listing{
			{ID: "lst-s1", CatalogItemID: "cat-1", NodeID: "fc-a", OwnerRef: "sel-1", AvailableQty: 6},
			{ID: "lst-s2", CatalogItemID: "cat-1", NodeID: "fc-a", OwnerRef: "sel-2", AvailableQty: 6},
		}
		m, ok := runMerge(t, w, policy(5), member("asg-1", 4), member("asg-2", 4))
		wantFollowers(t, m, ok)
	})
	t.Run("a different owner per item", func(t *testing.T) {
		w := mergeWorld()
		w.Listings = []network.Listing{
			listing("fc-a", "cat-1", 10),
			{ID: "lst-cat-2-sel", CatalogItemID: "cat-2", NodeID: "fc-a", OwnerRef: "sel-1", AvailableQty: 10},
		}
		carrier := member("asg-1", 3)
		c := member("asg-2", 3)
		c.lines = []assignment.Line{{LineNo: 1, CatalogItemID: "cat-2", Quantity: 3}}
		m, ok := runMerge(t, w, policy(5), carrier, c)
		wantFollowers(t, m, ok, "asg-2")
		if m.Lines["asg-1"][0].OwnerRef != "" || m.Lines["asg-2"][0].OwnerRef != "sel-1" {
			t.Fatalf("owners = %q and %q", m.Lines["asg-1"][0].OwnerRef, m.Lines["asg-2"][0].OwnerRef)
		}
	})
	t.Run("a discontinued item", func(t *testing.T) {
		w := mergeWorld()
		w.Discontinued = map[string]bool{"cat-1": true}
		if _, ok := ResolveMerged(w, "fc-a", []*assignment.Assignment{member("asg-1", 1).build(t)}); ok {
			t.Fatal("a discontinued item must not resolve")
		}
	})
}

func TestMergeCaps(t *testing.T) {
	cands := []spec{member("asg-2", 1), member("asg-3", 1), member("asg-4", 1)}
	t.Run("two members", func(t *testing.T) {
		m, ok := runMerge(t, mergeWorld(), policy(2), member("asg-1", 1), cands...)
		wantFollowers(t, m, ok, "asg-2")
	})
	t.Run("ten members", func(t *testing.T) {
		m, ok := runMerge(t, mergeWorld(), policy(10), member("asg-1", 1), cands...)
		wantFollowers(t, m, ok, "asg-2", "asg-3", "asg-4")
	})
	t.Run("policy off", func(t *testing.T) {
		for _, n := range []int{0, 1} {
			m, ok := runMerge(t, mergeWorld(), policy(n), member("asg-1", 1), cands...)
			wantFollowers(t, m, ok)
		}
	})
	t.Run("only twice the cap is considered", func(t *testing.T) {
		bad := member("x", 1)
		bad.key = ""
		in := []spec{bad, bad, bad, bad, member("asg-9", 1)}
		m, ok := runMerge(t, mergeWorld(), policy(2), member("asg-1", 1), in...)
		wantFollowers(t, m, ok)
	})
	t.Run("a candidate that breaks the line cap is skipped", func(t *testing.T) {
		big := member("asg-2", 1)
		for i := 2; i <= assignment.MaxLines; i++ {
			big.lines = append(big.lines, assignment.Line{LineNo: i, CatalogItemID: "cat-1", Quantity: 1})
		}
		m, ok := runMerge(t, mergeWorld(), policy(5), member("asg-1", 1), big, member("asg-3", 1))
		wantFollowers(t, m, ok, "asg-3")
	})
}

func TestMergeSkipsAnUnfittingCandidateAndKeepsTheNext(t *testing.T) {
	late := member("asg-2", 1)
	late.earliest = h(27) // no wave inside its window
	m, ok := runMerge(t, mergeWorld(), policy(5), member("asg-1", 1), late, member("asg-3", 1))
	wantFollowers(t, m, ok, "asg-3")
}

func TestNotBeforeZeroLeavesTheSearchUnchanged(t *testing.T) {
	for name, w := range fixtures() {
		t.Run(name, func(t *testing.T) {
			base := request()
			early := base
			early.NotBefore = h(-1000)
			a, b := Search(w, base), Search(w, early)
			if len(a) != len(b) {
				t.Fatalf("%d options against %d", len(a), len(b))
			}
			for i := range a {
				if !a[i].Candidate.Same(b[i].Candidate) || !a[i].ReleaseAt.Equal(b[i].ReleaseAt) {
					t.Fatalf("option %d differs", i)
				}
			}
		})
	}
}

func TestNotBeforeSkipsEarlierWaves(t *testing.T) {
	w := mergeWorld()
	r := request()
	r.DeliverByLatest = h(60)
	best, ok := Best(w, r)
	if !ok || best.Candidate.DeliveryWaveID != "w1" {
		t.Fatalf("without a bound the earliest wave wins, got %+v", best.Candidate)
	}
	r.NotBefore = h(27)
	best, ok = Best(w, r)
	if !ok || best.Candidate.DeliveryWaveID != "w2" {
		t.Fatalf("with NotBefore the wave ending after it wins, got %+v", best.Candidate)
	}
	r.NotBefore = h(51)
	if _, ok = Best(w, r); ok {
		t.Fatal("no wave ends after the bound")
	}
	r.NotBefore = h(26)
	if best, _ = Best(w, r); best.Candidate.DeliveryWaveID != "w1" {
		t.Fatal("a wave ending exactly at the bound is allowed")
	}
}

func TestSumLinesGroupsByItem(t *testing.T) {
	a := member("asg-1", 2).build(t)
	b := member("asg-2", 3)
	b.lines = []assignment.Line{{LineNo: 1, CatalogItemID: "cat-1", Quantity: 3}, {LineNo: 2, CatalogItemID: "cat-0", Quantity: 1}}
	got := SumLines([]*assignment.Assignment{a, b.build(t)})
	if len(got) != 2 || got[0].CatalogItemID != "cat-0" || got[1].CatalogItemID != "cat-1" || got[1].Quantity != 5 || got[0].Quantity != 1 {
		t.Fatalf("sums = %+v", got)
	}
	if !policy(2).ConsolidationOn() || policy(1).ConsolidationOn() || policy(0).ConsolidationOn() {
		t.Fatal("consolidation is on from two members")
	}
}
