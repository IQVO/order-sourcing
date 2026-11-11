package sourcing

import (
	"testing"
	"time"

	"github.com/claudioed/order-sourcing/internal/domain/network"
)

// holdWorld prices both nodes the same and gives the cross-region fc-b the later
// pickup (cutoff 6 against 4) onto the same onward leg and wave, so only the hold
// separates them: the legacy score ranks fc-a first (the cross-region penalty).
func holdWorld() World {
	w := costedWorld(500, 500)
	for i, d := range w.Departures {
		if d.ID == "pk-b" {
			w.Departures[i] = dep("pk-b", "lane-b", "fc-b", "sc-1", 6, 7, 9)
		}
	}
	return w
}

func TestHoldLatestPickupPrefersTheLaterCutoffAtEqualCost(t *testing.T) {
	w := holdWorld()
	r := request()
	plain := Rank(w, r).Options
	if plain[0].Candidate.NodeID != "fc-a" {
		t.Fatalf("without the hold the legacy score ranks the in-region node first: %v", nodesOf(plain))
	}
	r.HoldLatestPickup = true
	held := Rank(w, r).Options
	best := held[0]
	if best.Candidate.NodeID != "fc-b" || !best.Candidate.PickupCutoffAt.Equal(h(6)) {
		t.Fatalf("with the hold the later cutoff wins at equal cost: %v best %+v", nodesOf(held), best.Candidate)
	}
	// The release point follows the held pickup: later by the two hours of cutoff.
	if got := best.ReleaseAt.Sub(held[1].ReleaseAt); got != 2*time.Hour {
		t.Fatalf("the hold moves the release point by %v", got)
	}
}

func TestHoldNeverBeatsMoney(t *testing.T) {
	// fc-b has the later pickup but costs one cent more: the money still decides.
	w := holdWorld()
	w.Lanes["lane-b"] = declaredLane("lane-b", 501)
	r := request()
	r.HoldLatestPickup = true
	if best := Rank(w, r).Options[0]; best.Candidate.NodeID != "fc-a" {
		t.Fatalf("the cheaper node wins over a later pickup: %+v", best.Candidate)
	}
}

func TestHoldChangesNothingWhenEveryCutoffIsTheSame(t *testing.T) {
	for name, w := range fixtures() {
		t.Run(name, func(t *testing.T) {
			r := request()
			a := Rank(w, r).Options
			r.HoldLatestPickup = true
			b := Rank(w, r).Options
			if len(a) != len(b) {
				t.Fatalf("%d options against %d", len(a), len(b))
			}
			for i := range a {
				if !a[i].Candidate.Same(b[i].Candidate) {
					t.Fatalf("option %d differs: %+v against %+v", i, a[i].Candidate, b[i].Candidate)
				}
			}
		})
	}
}

func TestHoldReachesTheChosenDay(t *testing.T) {
	// Two days of waves; the window starts on the second: the hold picks the
	// latest pickup that still reaches that day's wave, never the first day's.
	w := holdWorld()
	w.Waves = []network.Wave{wave("w1", 15, 20, 26), wave("w2", 39, 40, 44)}
	r := request()
	r.DeliverByLatest, r.NotBefore, r.HoldLatestPickup = h(60), h(40), true
	best, ok := Best(w, r)
	if !ok || best.Candidate.DeliveryWaveID != "w2" || best.Candidate.PickupDepartureID != "pk-b" || !best.Candidate.ETA.Equal(h(44)) {
		t.Fatalf("best = %+v ok=%v", best.Candidate, ok)
	}
	r.NotBefore = time.Time{}
	if best, _ = Best(w, r); best.Candidate.DeliveryWaveID != "w1" {
		t.Fatalf("with no window start the earliest wave stays: %+v", best.Candidate)
	}
}
