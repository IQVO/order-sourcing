package sourcing

import (
	"slices"
	"testing"
	"time"

	"github.com/claudioed/order-sourcing/internal/domain/assignment"
	"github.com/claudioed/order-sourcing/internal/domain/network"
)

func TestSearchRanksInRegionFirstAndPricesThePath(t *testing.T) {
	opts := Search(world(), request())
	wantNodes(t, opts, "fc-a", "fc-b")
	// 1 leg (100) + 15 hours from the pickup departure (h5) to the wave start (h20) = 150 -> 250; fc-b adds the 1000 cross-region penalty.
	wantCost(t, opts[0], 250)
	wantCost(t, opts[1], 1250)
	c := opts[0].Candidate
	if c.PickupDepartureID != "pk-a" || len(c.Legs) != 1 || c.Legs[0] != "lh-1" || c.DeliveryWaveID != "w1" || !c.ETA.Equal(h(26)) || !c.PickupCutoffAt.Equal(h(4)) {
		t.Fatalf("candidate = %+v", c)
	}
	// releaseAt = cutoff h4 - 60 min handling - 30 min ack budget, but never before now.
	if want := h(4).Add(-90 * 60 * 1e9); !opts[0].ReleaseAt.Equal(want) {
		t.Fatalf("releaseAt = %v, want %v", opts[0].ReleaseAt, want)
	}
}

func TestBestAndEmpty(t *testing.T) {
	o, ok := Best(world(), request())
	if !ok || o.Candidate.NodeID != "fc-a" {
		t.Fatalf("best = %+v %v", o, ok)
	}
	w := world()
	w.Area = network.ServiceArea{}
	if _, ok := Best(w, request()); ok {
		t.Fatal("an unknown destination yields no candidate")
	}
	if got := Search(w, request()); got != nil {
		t.Fatalf("search = %v", got)
	}
}

func TestTieGoesToTheWitnessThenNodeIDThenPickup(t *testing.T) {
	w := world()
	w.Nodes["fc-b"] = fc("fc-b", "r1")
	opts := Search(w, request())
	wantNodes(t, opts, "fc-a", "fc-b")
	r := request()
	r.WitnessNodeID = "fc-b"
	wantNodes(t, Search(w, r), "fc-b", "fc-a")
	r.WitnessNodeID = ""
	wantNodes(t, Search(w, r), "fc-a", "fc-b")
	r.WitnessNodeID = "fc-z"
	wantNodes(t, Search(w, r), "fc-a", "fc-b")
	// Two pickups of one node with equal cost: the lower departure id first.
	w2 := world()
	w2.Departures = append(w2.Departures, dep("pk-a0", "lane-a", "fc-a", "sc-1", 4, 5, 8))
	o := Search(w2, request())
	if o[0].Candidate.PickupDepartureID != "pk-a" && o[0].Candidate.PickupDepartureID != "pk-a0" {
		t.Fatal("setup")
	}
	if o[0].Candidate.NodeID != "fc-a" || o[0].Candidate.PickupDepartureID != "pk-a" || o[1].Candidate.PickupDepartureID != "pk-a0" {
		t.Fatalf("pickups = %s, %s", o[0].Candidate.PickupDepartureID, o[1].Candidate.PickupDepartureID)
	}
}

func TestNodeEligibilityAndExclusion(t *testing.T) {
	cases := map[string]func(*World, *Request){
		"suspended": func(w *World, _ *Request) {
			n := w.Nodes["fc-a"]
			n.Status = network.StatusSuspended
			w.Nodes["fc-a"] = n
		},
		"no profile": func(w *World, _ *Request) { n := w.Nodes["fc-a"]; n.ProfileRevision = 0; w.Nodes["fc-a"] = n },
		"excluded": func(_ *World, r *Request) {
			r.Excluded = []assignment.ExcludedNode{{NodeID: "fc-a", Reason: "REJECTED"}}
		},
	}
	for name, mut := range cases {
		t.Run(name, func(t *testing.T) {
			w, r := world(), request()
			mut(&w, &r)
			wantNodes(t, Search(w, r), "fc-b")
		})
	}
}

func TestLineAvailability(t *testing.T) {
	cases := map[string]func(*World){
		"short":        func(w *World) { w.Listings[0].AvailableQty = 1 },
		"suppressed":   func(w *World) { w.Listings[0].Suppressed = true },
		"discontinued": func(w *World) { w.Discontinued["cat-1"] = true },
		"no listing":   func(w *World) { w.Listings = w.Listings[1:] },
	}
	for name, mut := range cases {
		t.Run(name, func(t *testing.T) {
			w := world()
			mut(&w)
			got := nodesOf(Search(w, request()))
			if name == "discontinued" {
				if len(got) != 0 {
					t.Fatalf("nodes = %v", got)
				}
				return
			}
			if len(got) != 1 || got[0] != "fc-b" {
				t.Fatalf("nodes = %v", got)
			}
		})
	}
	w := world()
	w.Listings[0].AvailableQty = 2
	wantNodes(t, Search(w, request()), "fc-a", "fc-b") // exactly the quantity is enough
}

func TestResolveLines(t *testing.T) {
	w := world()
	lines := []assignment.Line{{LineNo: 1, CatalogItemID: "cat-1", Quantity: 2}}
	got, ok := ResolveLines(w, "fc-a", lines)
	if !ok || got[0].ListingID != "lst-cat-1-fc-a" || lines[0].ListingID != "" {
		t.Fatalf("lines = %+v ok=%v (input must not change)", got, ok)
	}
	if _, ok := ResolveLines(w, "fc-z", lines); ok {
		t.Fatal("a node without listings cannot ship")
	}
	w.Discontinued["cat-1"] = true
	if _, ok := ResolveLines(w, "fc-a", lines); ok {
		t.Fatal("a discontinued item cannot ship")
	}
}

func TestPickupFeasibility(t *testing.T) {
	w := world()
	// cutoff h4 minus handling 60 min = h3: ready exactly at the cutoff is allowed, one minute later is not.
	r := request()
	r.Now = h(3)
	wantNodes(t, Search(w, r), "fc-a", "fc-b")
	r.Now = h(3).Add(time.Minute)
	wantNodes(t, Search(w, r))
	// releaseAt is clamped to now once the natural point has passed.
	r.Now = h(3)
	if got := Search(w, r)[0].ReleaseAt; !got.Equal(h(3)) {
		t.Fatalf("releaseAt = %v, want now", got)
	}
}

func TestDepartureAndLaneFilters(t *testing.T) {
	cases := map[string]func(*World){
		"cancelled pickup": func(w *World) { w.Departures[0].Cancelled = true },
		"full pickup":      func(w *World) { w.Departures[0].Remaining = 0 },
		"unscheduled":      func(w *World) { w.Departures[0].Scheduled = false },
		"closed lane":      func(w *World) { w.Lanes["lane-a"] = network.Lane{ID: "lane-a", Closed: true} },
	}
	for name, mut := range cases {
		t.Run(name, func(t *testing.T) {
			w := world()
			mut(&w)
			wantNodes(t, Search(w, request()), "fc-b")
		})
	}
	w := world()
	w.Lanes["lane-a"] = network.Lane{ID: "lane-a"}
	wantNodes(t, Search(w, request()), "fc-a", "fc-b")
	// The onward leg being unusable removes every candidate.
	w = world()
	w.Departures[2].Cancelled = true
	wantNodes(t, Search(w, request()))
}

func TestOnwardLegNeedsTheSortDwell(t *testing.T) {
	w := world()
	// arrival h8 + dwell 30 min = h8:30; a cutoff at h8 is too early, at h9 fine.
	w.Departures[2] = dep("lh-1", "lane-s", "sc-1", "ds-1", 8, 11, 14)
	wantNodes(t, Search(w, request()))
	w.Departures[2] = dep("lh-1", "lane-s", "sc-1", "ds-1", 9, 11, 14)
	wantNodes(t, Search(w, request()), "fc-a", "fc-b")
	n := w.Nodes["sc-1"]
	n.SortDwellMinutes = 0
	w.Nodes["sc-1"] = n
	w.Departures[2] = dep("lh-1", "lane-s", "sc-1", "ds-1", 8, 11, 14)
	wantNodes(t, Search(w, request()), "fc-a", "fc-b") // no dwell: cutoff == arrival is enough
}

func TestWaveSelection(t *testing.T) {
	w := world()
	w.Waves = []network.Wave{wave("w2", 15, 30, 36), wave("w1", 15, 20, 26), wave("w0", 13, 10, 16)}
	o := Search(w, request())[0]
	if o.Candidate.DeliveryWaveID != "w1" {
		t.Fatalf("wave = %s: earliest usable wave whose induct cutoff the arrival (h14) meets", o.Candidate.DeliveryWaveID)
	}
	w.Waves[1].Remaining = 0
	if got := Search(w, request())[0].Candidate.DeliveryWaveID; got != "w2" {
		t.Fatalf("wave = %s, a full wave is skipped", got)
	}
	w.Waves[0].Cancelled = true
	wantNodes(t, Search(w, request()))
	w = world()
	other := wave("wx", 15, 20, 25)
	other.StationID = "ds-2"
	w.Waves = []network.Wave{other}
	wantNodes(t, Search(w, request()))
	// Equal windows: the lower wave id wins.
	w = world()
	w.Waves = []network.Wave{wave("wb", 15, 20, 26), wave("wa", 15, 20, 26)}
	if got := Search(w, request())[0].Candidate.DeliveryWaveID; got != "wa" {
		t.Fatalf("wave = %s", got)
	}
	// Induct cutoff exactly at the arrival is allowed.
	w = world()
	w.Waves = []network.Wave{wave("w1", 14, 20, 26)}
	wantNodes(t, Search(w, request()), "fc-a", "fc-b")
	w.Waves = []network.Wave{wave("w1", 13, 20, 26)}
	wantNodes(t, Search(w, request()))
}

func TestDeliverByLatestBoundary(t *testing.T) {
	r := request()
	r.DeliverByLatest = h(26)
	wantNodes(t, Search(world(), r), "fc-a", "fc-b") // eta == deliverByLatest is allowed
	r.DeliverByLatest = h(26).Add(-time.Second)
	wantNodes(t, Search(world(), r))
}

func TestScorecardAndBookPressure(t *testing.T) {
	w := world()
	w.Nodes["fc-b"] = fc("fc-b", "r1")
	base := Search(w, request())
	wantCost(t, base[0], 250)
	for grade, penalty := range map[string]int64{"A": 0, "B": 50, "C": 200, "D": 500, "PROVISIONAL": 0} {
		w.Scorecards = map[string]network.Scorecard{"fc-a": {NodeID: "fc-a", Window: network.Window30d, Grade: grade}}
		wantCost(t, find(t, Search(w, request()), "fc-a"), 250+penalty)
	}
	w.Scorecards = map[string]network.Scorecard{"fc-a": {NodeID: "fc-a", Window: "7d", Grade: "D"}}
	wantCost(t, find(t, Search(w, request()), "fc-a"), 250)
	w.Scorecards = nil
	w.Pressure = []network.Pressure{{BookID: "b1", NodeID: "fc-a", PickupDepartureID: "pk-a"}}
	wantCost(t, find(t, Search(w, request()), "fc-a"), 750)
	w.Pressure = []network.Pressure{{BookID: "b1", NodeID: "fc-a", PickupDepartureID: "pk-other"}, {BookID: "b2", NodeID: "fc-b", PickupDepartureID: "pk-a"}}
	wantCost(t, find(t, Search(w, request()), "fc-a"), 250)
	// Pressure moves the winner to the other node.
	w.Pressure = []network.Pressure{{BookID: "b1", NodeID: "fc-a", PickupDepartureID: "pk-a"}}
	wantNodes(t, Search(w, request()), "fc-b", "fc-a")
}

func find(t *testing.T, opts []Option, node string) Option {
	t.Helper()
	for _, o := range opts {
		if o.Candidate.NodeID == node {
			return o
		}
	}
	t.Fatalf("no option for %s in %v", node, nodesOf(opts))
	return Option{}
}

func TestRegionPenaltyNeedsBothRegions(t *testing.T) {
	w := world()
	w.Area.RegionID = ""
	wantCost(t, find(t, Search(w, request()), "fc-b"), 250)
	w = world()
	n := w.Nodes["fc-b"]
	n.RegionID = ""
	w.Nodes["fc-b"] = n
	wantCost(t, find(t, Search(w, request()), "fc-b"), 250)
}

func TestSameDayNeedsASubSameDayNodeAndADirectPickup(t *testing.T) {
	w := world()
	direct := dep("pk-d", "lane-d", "fc-a", "ds-1", 4, 5, 7)
	w.Departures = append(w.Departures, direct)
	r := request()
	r.Tier = assignment.TierSameDay
	wantNodes(t, Search(w, r)) // fc-a is SORTABLE, not SUB_SAME_DAY
	for _, sub := range []string{network.SubtypeSubSameDay, network.SubtypeMicro} {
		n := w.Nodes["fc-a"]
		n.Subtype = sub
		w.Nodes["fc-a"] = n
		opts := Search(w, r)
		wantNodes(t, opts, "fc-a")
		c := opts[0].Candidate
		if c.PickupDepartureID != "pk-d" || len(c.Legs) != 0 || !opts[0].ReleaseAt.Equal(now) {
			t.Fatalf("%s: candidate %+v releaseAt %v (SAME_DAY: direct pickup, release at once)", sub, c, opts[0].ReleaseAt)
		}
		// 0 legs + (wave start h20 - departs h5) = 15 hours.
		wantCost(t, opts[0], 150)
	}
}

func TestPathDepthAndLoops(t *testing.T) {
	w := world()
	w.Nodes["sc-2"] = network.Node{ID: "sc-2", Type: "SORTATION_CENTER", Operator: "NETWORK", Status: network.StatusActive}
	w.Departures = []network.Departure{
		dep("pk-a", "l", "fc-a", "sc-1", 4, 5, 6),
		dep("x1", "l", "sc-1", "sc-2", 7, 7, 8),
		dep("loop", "l", "sc-2", "sc-1", 9, 9, 10), // back to a visited node: never taken
		dep("x2", "l", "sc-2", "ds-1", 9, 10, 12),
	}
	opts := Search(w, request())
	wantNodes(t, opts, "fc-a")
	if got := opts[0].Candidate.Legs; len(got) != 2 || got[0] != "x1" || got[1] != "x2" {
		t.Fatalf("legs = %v", got)
	}
	wantCost(t, opts[0], 2*100+10*15)
	// The pickup plus four onward departures is the longest path (MaxDepartures).
	chain := []network.Departure{dep("pk-a", "l", "fc-a", "n1", 4, 5, 6)}
	prev := "n1"
	for i := 1; i <= 4; i++ {
		next := "n" + string(rune('1'+i))
		if i == 4 {
			next = "ds-1"
		}
		chain = append(chain, dep("c"+string(rune('0'+i)), "l", prev, next, 7+i, 7+i, 8+i))
		w.Nodes[prev] = network.Node{ID: prev, Type: "SORTATION_CENTER", Status: network.StatusActive}
		prev = next
	}
	w.Departures = chain
	if len(Search(w, request())) != 1 || len(Search(w, request())[0].Candidate.Legs) != 4 {
		t.Fatalf("a path of exactly %d departures must be found", MaxDepartures)
	}
	long := append(slices.Clone(chain[:4]), dep("c3b", "l", "n4", "n5", 20, 20, 21), dep("c4b", "l", "n5", "ds-1", 22, 22, 23))
	w.Nodes["n5"] = network.Node{ID: "n5", Type: "SORTATION_CENTER", Status: network.StatusActive}
	w.Departures = long
	w.Waves = []network.Wave{wave("late", 100, 120, 126)} // the late arrival (h23) still meets this wave: only the length rules the path out
	r := request()
	r.DeliverByLatest = h(200)
	if got := Search(w, r); len(got) != 0 {
		t.Fatalf("a path of %d departures is too long: %v", MaxDepartures+1, got)
	}
	w.Departures = chain
	if got := Search(w, r); len(got) != 1 {
		t.Fatalf("the %d-departure path reaches the same wave: %v", MaxDepartures, got)
	}
	w.Departures = long
	if got := Search(w, request()); len(got) != 0 {
		t.Fatalf("a path of %d departures is too long: %v", MaxDepartures+1, got)
	}
}

func TestCheapestPathWins(t *testing.T) {
	w := world()
	w.Departures = append(w.Departures, dep("pk-a-direct", "l", "fc-a", "ds-1", 4, 5, 7))
	opts := Search(w, request())
	var direct, viaSC *Option
	for i := range opts {
		switch opts[i].Candidate.PickupDepartureID {
		case "pk-a-direct":
			direct = &opts[i]
		case "pk-a":
			viaSC = &opts[i]
		}
	}
	if direct == nil || viaSC == nil || direct.Candidate.Cost >= viaSC.Candidate.Cost || len(direct.Candidate.Legs) != 0 {
		t.Fatalf("direct %v via %v", direct, viaSC)
	}
	// Among several onward departures the cheaper (fewer legs, then earlier wave, then lower legs) path is kept.
	w = world()
	w.Departures = append(w.Departures, dep("lh-0", "lane-s", "sc-1", "ds-1", 10, 11, 14))
	if got := Search(w, request())[0].Candidate.Legs; len(got) != 1 || got[0] != "lh-0" {
		t.Fatalf("equal paths: the lower leg id wins, got %v", got)
	}
}

func TestPathTiebreakOnWaveBeforeLegs(t *testing.T) {
	a := path{legs: []string{"b"}, wave: network.Wave{WindowEnd: h(20)}, cost: 100}
	b := path{legs: []string{"a"}, wave: network.Wave{WindowEnd: h(21)}, cost: 100}
	if !better(a, b) || better(b, a) {
		t.Fatal("the earlier wave beats the lower leg id")
	}
	if !better(path{cost: 1}, path{cost: 2}) || better(path{cost: 2}, path{cost: 1}) || better(path{}, path{}) {
		t.Fatal("cost orders first and equal paths are not better")
	}
}

// declaredLane is a lane row known only through its cost declaration (a stub
// LaneCostDeclared may leave behind) or a fully known lane with a cost.
func declaredLane(id string, cents int64) network.Lane {
	var l network.Lane
	l.ID = id
	l.ApplyCostDeclared(cents, h(1))
	return l
}

func withWaveCost(w World, cents int64) World {
	w.Waves[0].ApplyCostDeclared(cents, h(1))
	return w
}

// TestMoneyBeatsLegs proves the inversion: a two-leg path whose declared cents
// are lower wins over a one-leg path the ordinal cost (fewer legs) ranked
// first before ADR 0011.
func TestMoneyBeatsLegs(t *testing.T) {
	w := world()
	// fc-c reaches ds-1 through sc-x and sc-1: two onward legs, cheap lanes.
	w.Nodes["fc-c"] = fc("fc-c", "r1")
	w.Nodes["sc-x"] = network.Node{ID: "sc-x", Type: "SORTATION_CENTER", Operator: "NETWORK", Status: network.StatusActive, SortDwellMinutes: 30}
	w.Listings = append(w.Listings, listing("fc-c", "cat-1", 10))
	w.Departures = append(w.Departures,
		dep("pk-c", "lane-c", "fc-c", "sc-x", 4, 5, 7),
		dep("x-1", "lane-x", "sc-x", "sc-1", 8, 9, 10),
		dep("lh-2", "lane-s2", "sc-1", "ds-1", 11, 12, 15),
	)
	w.Lanes["lane-a"], w.Lanes["lane-b"] = declaredLane("lane-a", 900), declaredLane("lane-b", 500)
	w.Lanes["lane-c"], w.Lanes["lane-x"] = declaredLane("lane-c", 30), declaredLane("lane-x", 20)
	w.Lanes["lane-s"], w.Lanes["lane-s2"] = declaredLane("lane-s", 50), declaredLane("lane-s2", 40)
	w = withWaveCost(w, 0) // a declared zero is a complete component

	opts := Search(w, request())
	// Money: fc-c 30+20+40+0 = 90 < fc-b 500+40 = 540 < fc-a 900+40 = 940.
	// The ordinal score alone would order fc-a (250) < fc-c (350) < fc-b (1250).
	wantNodes(t, opts, "fc-c", "fc-b", "fc-a")
	wantCost(t, opts[0], 350) // 2 legs + 15 queue hours: the legacy score is only the tie-break now
	if !opts[0].Candidate.CostComplete || opts[0].Candidate.EstimatedCostCents != 90 {
		t.Fatalf("fc-c estimate = %d complete %v", opts[0].Candidate.EstimatedCostCents, opts[0].Candidate.CostComplete)
	}
	if opts[2].Candidate.EstimatedCostCents != 940 {
		t.Fatalf("fc-a estimate = %d", opts[2].Candidate.EstimatedCostCents)
	}
}

// TestIncompleteEstimateStillCompetes: an undeclared component contributes 0
// and the estimate still competes by its sum — it is never skipped or ranked
// last (ADR 0011).
func TestIncompleteEstimateStillCompetes(t *testing.T) {
	w := world()
	w.Lanes["lane-a"], w.Lanes["lane-s"] = declaredLane("lane-a", 900), declaredLane("lane-s", 50)
	w = withWaveCost(w, 0)
	opts := Search(w, request())
	// fc-b's pickup lane is undeclared: estimate 50 (only the declared linehaul
	// counts) and incomplete, but its sum still wins over fc-a's declared 950
	// even though fc-b's legacy score is worse.
	wantNodes(t, opts, "fc-b", "fc-a")
	if opts[0].Candidate.EstimatedCostCents != 50 || opts[0].Candidate.CostComplete {
		t.Fatalf("fc-b estimate = %d complete %v", opts[0].Candidate.EstimatedCostCents, opts[0].Candidate.CostComplete)
	}
	if !opts[1].Candidate.CostComplete || opts[1].Candidate.EstimatedCostCents != 950 {
		t.Fatalf("fc-a estimate = %d complete %v", opts[1].Candidate.EstimatedCostCents, opts[1].Candidate.CostComplete)
	}
}

// TestDeclaredZeroIsComplete: a declared 0 and an undeclared component both
// contribute 0 cents; completeness is the only difference, and a money tie
// falls to the legacy score.
func TestDeclaredZeroIsComplete(t *testing.T) {
	w := world()
	w.Lanes["lane-a"], w.Lanes["lane-s"] = declaredLane("lane-a", 0), declaredLane("lane-s", 0)
	w = withWaveCost(w, 0)
	opts := Search(w, request())
	wantNodes(t, opts, "fc-a", "fc-b") // 0 cents both: the legacy score decides (fc-b carries the region penalty)
	if !opts[0].Candidate.CostComplete || opts[0].Candidate.EstimatedCostCents != 0 {
		t.Fatalf("fc-a estimate = %d complete %v", opts[0].Candidate.EstimatedCostCents, opts[0].Candidate.CostComplete)
	}
	if opts[1].Candidate.CostComplete {
		t.Fatal("an undeclared lane leaves the estimate incomplete")
	}
}

// TestMoneyTieFallsToLegacyThenWitness: equal cents order by the legacy
// penalty score; equal cents and score by the witness node.
func TestMoneyTieFallsToLegacyThenWitness(t *testing.T) {
	w := world()
	w.Lanes["lane-a"], w.Lanes["lane-b"], w.Lanes["lane-s"] = declaredLane("lane-a", 300), declaredLane("lane-b", 300), declaredLane("lane-s", 50)
	w = withWaveCost(w, 0)
	r := request()
	r.WitnessNodeID = "fc-b"
	// Equal money (350); fc-b's legacy score is worse (cross-region), so it
	// loses even though it is the witness.
	wantNodes(t, Search(w, r), "fc-a", "fc-b")
	// Equal money and equal legacy (fc-b in the same region): the witness wins.
	w.Nodes["fc-b"] = fc("fc-b", "r1")
	wantNodes(t, Search(w, r), "fc-b", "fc-a")
}

// TestUndeclaredWaveLeavesEstimateIncomplete: the wave term completes the
// estimate only when a stop cost was declared for it.
func TestUndeclaredWaveLeavesEstimateIncomplete(t *testing.T) {
	w := world()
	w.Lanes["lane-a"], w.Lanes["lane-s"] = declaredLane("lane-a", 300), declaredLane("lane-s", 50)
	var est int64
	var complete, found bool
	for _, o := range Search(w, request()) {
		if o.Candidate.NodeID == "fc-a" {
			est, complete, found = o.Candidate.EstimatedCostCents, o.Candidate.CostComplete, true
		}
	}
	if !found {
		t.Fatal("fc-a has no candidate")
	}
	if est != 350 || complete {
		t.Fatalf("estimate = %d complete %v", est, complete)
	}
}
