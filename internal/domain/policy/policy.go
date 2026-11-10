// Package policy is the SourcingPolicy aggregate (gap G26, ADR 0014): a single
// versioned document that lets the operator trade a bounded cost deviation for
// in-region fulfilment. A published version is immutable; the current policy is
// the highest version, and version 0 is the built-in default (every lever off,
// so the candidate ordering is exactly that of ADR 0011).
package policy

import (
	"errors"
	"fmt"
	"time"

	"github.com/claudioed/order-sourcing/internal/domain/shared"
)

// Bounds of the policy parameters (plain literals, ADR 0014).
const (
	MaxSurchargeCents   = 100000
	MaxDeviationPercent = 200
	// MaxVersion bounds the version number to what the wire and the database hold.
	MaxVersion = 2147483647
)

// ErrVersionNotNext marks a publish whose version is not the stored one + 1.
var ErrVersionNotNext = errors.New("policy version is not the next one")

// Policy is the aggregate root.
type Policy struct {
	version      int
	crossRegion  int
	maxDeviation int
	publishedAt  time.Time
}

// Snapshot is the persisted form of a policy. Restore validates it.
type Snapshot struct {
	Version                   int
	CrossRegionSurchargeCents int
	MaxCostDeviationPercent   int
	PublishedAt               time.Time
}

// Input is the data of a publish: the whole document, every field an integer
// inside its range (0 switches the lever off).
type Input struct {
	CrossRegionSurchargeCents int
	MaxCostDeviationPercent   int
}

// Default returns version 0: the policy in force before anything is published.
func Default() *Policy { return &Policy{} }

func validate(in Input) error {
	if in.CrossRegionSurchargeCents < 0 || in.CrossRegionSurchargeCents > MaxSurchargeCents {
		return shared.Invalid("crossRegionSurchargeCents must be between 0 and %d", MaxSurchargeCents)
	}
	if in.MaxCostDeviationPercent < 0 || in.MaxCostDeviationPercent > MaxDeviationPercent {
		return shared.Invalid("maxCostDeviationPercent must be between 0 and %d", MaxDeviationPercent)
	}
	return nil
}

// Publish creates policy version current+1 (current is the highest stored
// version, 0 when none) and raises SourcingPolicyPublished.
func Publish(current int, in Input, now time.Time) (*Policy, []shared.Event, error) {
	if current < 0 || current >= MaxVersion {
		return nil, nil, fmt.Errorf("%w: current version %d", ErrVersionNotNext, current)
	}
	if err := validate(in); err != nil {
		return nil, nil, err
	}
	p := &Policy{
		version: current + 1, crossRegion: in.CrossRegionSurchargeCents, maxDeviation: in.MaxCostDeviationPercent,
		publishedAt: shared.UTC(now),
	}
	ev := SourcingPolicyPublished{
		Header: shared.Header{Version: int64(p.version), At: p.publishedAt}, Number: p.version,
		CrossRegionSurchargeCents: p.crossRegion, MaxCostDeviationPercent: p.maxDeviation, PublishedAt: p.publishedAt,
	}
	return p, []shared.Event{ev}, nil
}

// Restore rebuilds a persisted policy, validating every invariant. Version 0
// is only valid as the default.
func Restore(s Snapshot) (*Policy, error) {
	if err := validate(Input{CrossRegionSurchargeCents: s.CrossRegionSurchargeCents, MaxCostDeviationPercent: s.MaxCostDeviationPercent}); err != nil {
		return nil, shared.Corrupt("policy %d: %v", s.Version, err)
	}
	if s.Version < 1 || s.Version > MaxVersion || s.PublishedAt.IsZero() {
		return nil, shared.Corrupt("policy %d: a version of at least 1 and publishedAt are required", s.Version)
	}
	return &Policy{
		version: s.Version, crossRegion: s.CrossRegionSurchargeCents, maxDeviation: s.MaxCostDeviationPercent,
		publishedAt: shared.UTC(s.PublishedAt),
	}, nil
}

// Version returns the version number (0 for the default).
func (p *Policy) Version() int { return p.version }

// CrossRegionSurchargeCents returns the surcharge added to a candidate whose
// node region differs from the delivery area region.
func (p *Policy) CrossRegionSurchargeCents() int { return p.crossRegion }

// MaxCostDeviationPercent returns the regional budget over the cheapest
// effective cost.
func (p *Policy) MaxCostDeviationPercent() int { return p.maxDeviation }

// PublishedAt returns when the version was published (zero for the default).
func (p *Policy) PublishedAt() time.Time { return p.publishedAt }

// Snapshot returns the persisted form.
func (p *Policy) Snapshot() Snapshot {
	return Snapshot{
		Version: p.version, CrossRegionSurchargeCents: p.crossRegion, MaxCostDeviationPercent: p.maxDeviation,
		PublishedAt: p.publishedAt,
	}
}
