package clock_test

import (
	"testing"
	"time"

	"github.com/claudioed/order-sourcing/internal/adapters/outbound/clock"
)

func TestSystemNowIsUTCAndMicrosecondPrecise(t *testing.T) {
	before := time.Now().UTC().Add(-time.Second)
	got := clock.System{}.Now()
	after := time.Now().UTC().Add(time.Second)

	if got.Location() != time.UTC {
		t.Fatalf("location = %v, want UTC", got.Location())
	}
	if got.Nanosecond()%1000 != 0 {
		t.Fatalf("nanosecond = %d, want a whole microsecond", got.Nanosecond())
	}
	if got.Before(before) || got.After(after) {
		t.Fatalf("now = %v, not within [%v, %v]", got, before, after)
	}
}
