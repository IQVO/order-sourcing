// Package clock is the system-clock adapter of ports.Clock.
package clock

import (
	"time"

	"github.com/claudioed/order-sourcing/internal/application/ports"
)

// System returns the wall clock in UTC, truncated to the microsecond: that is
// the precision of Postgres' timestamptz, so a time stamped on an aggregate or
// an event reads back from the database unchanged.
type System struct{}

var _ ports.Clock = System{}

// Now implements ports.Clock.
func (System) Now() time.Time { return time.Now().UTC().Truncate(time.Microsecond) }
