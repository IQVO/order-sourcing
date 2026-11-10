package memory

import (
	"context"

	"github.com/claudioed/order-sourcing/internal/application/ports"
	"github.com/claudioed/order-sourcing/internal/domain/policy"
)

// PolicyRepository is the in-memory ports.PolicyRepository.
type PolicyRepository struct{ s *Store }

var _ ports.PolicyRepository = (*PolicyRepository)(nil)

// Current returns the highest version or ports.ErrNotFound.
func (r *PolicyRepository) Current(ctx context.Context) (*policy.Policy, error) {
	defer r.s.enter(ctx)()
	best := 0
	for v := range r.s.policies {
		best = max(best, v)
	}
	if best == 0 {
		return nil, ports.ErrNotFound
	}
	return policy.Restore(r.s.policies[best])
}

// Save stores a new version; an existing version is ports.ErrConcurrentUpdate.
func (r *PolicyRepository) Save(ctx context.Context, p *policy.Policy) error {
	defer r.s.enter(ctx)()
	snap := p.Snapshot()
	if _, exists := r.s.policies[snap.Version]; exists {
		return ports.ErrConcurrentUpdate
	}
	r.s.policies[snap.Version] = snap
	return nil
}
