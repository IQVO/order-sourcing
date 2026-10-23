package memory

import (
	"fmt"
	"sync"
	"time"

	"github.com/claudioed/order-sourcing/internal/domain/assignment"
)

// Clock is a settable ports.Clock.
type Clock struct {
	mu  sync.Mutex
	now time.Time
}

// NewClock returns a clock reading t.
func NewClock(t time.Time) *Clock { return &Clock{now: t} }

// Now returns the current reading.
func (c *Clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// Set moves the clock to t.
func (c *Clock) Set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = t
}

// Advance moves the clock forward by d.
func (c *Clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// SequentialIDs is a ports.IDGenerator minting asg-1, asg-2, ...
type SequentialIDs struct {
	mu sync.Mutex
	n  int
}

// NewAssignmentID returns the next assignment id.
func (g *SequentialIDs) NewAssignmentID() assignment.ID {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.n++
	return assignment.ID(fmt.Sprintf("asg-%d", g.n))
}
