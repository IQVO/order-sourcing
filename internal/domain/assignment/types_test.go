package assignment

import "testing"

func TestEnumsAreClosed(t *testing.T) {
	for _, s := range []State{StateQueued, StateReleased, StateLocked, StateShipped, StateDelivered, StateUnsourceable, StateCancelled} {
		if !s.Valid() {
			t.Errorf("state %s must be valid", s)
		}
	}
	for _, tier := range []Tier{TierSameDay, TierNextDay, TierTwoDay, TierStandard} {
		if !tier.Valid() {
			t.Errorf("tier %s must be valid", tier)
		}
	}
	for _, o := range []Outcome{OutcomePending, OutcomeAccepted, OutcomeRejected, OutcomeExpired} {
		if !o.Valid() {
			t.Errorf("outcome %s must be valid", o)
		}
	}
	if State("").Valid() || State("queued").Valid() || Tier("").Valid() || Tier("WEEKLY").Valid() || Outcome("").Valid() || Outcome("DONE").Valid() {
		t.Fatal("anything else is not a member")
	}
}

func TestTerminalAndCommittedStates(t *testing.T) {
	terminal := map[State]bool{StateDelivered: true, StateCancelled: true}
	committed := map[State]bool{StateLocked: true, StateShipped: true, StateDelivered: true}
	for _, s := range []State{StateQueued, StateReleased, StateLocked, StateShipped, StateDelivered, StateUnsourceable, StateCancelled} {
		if s.Terminal() != terminal[s] || s.Committed() != committed[s] {
			t.Errorf("%s: terminal=%v committed=%v", s, s.Terminal(), s.Committed())
		}
	}
}
