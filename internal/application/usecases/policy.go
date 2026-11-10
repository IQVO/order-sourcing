package usecases

import (
	"context"
	"errors"
	"fmt"

	"github.com/claudioed/order-sourcing/internal/application/ports"
	"github.com/claudioed/order-sourcing/internal/application/views"
	"github.com/claudioed/order-sourcing/internal/domain/policy"
	"github.com/claudioed/order-sourcing/internal/domain/sourcing"
)

// budgetKindRegion is the kind label of the budget-preferred counter (ADR 0014).
const budgetKindRegion = "region"

// current returns the highest published policy, the default (version 0) when
// none was published or no repository is wired.
func current(ctx context.Context, repo ports.PolicyRepository) (*policy.Policy, error) {
	if repo == nil {
		return policy.Default(), nil
	}
	p, err := repo.Current(ctx)
	switch {
	case err == nil:
		return p, nil
	case errors.Is(err, ports.ErrNotFound):
		return policy.Default(), nil
	default:
		return nil, fmt.Errorf("read sourcing policy: %w", err)
	}
}

// currentPolicy reads the policy for ONE search (ADR 0014): the version read
// here is stamped on every candidate of that search, and so on the release.
func (w writer) currentPolicy(ctx context.Context) (sourcing.Policy, error) {
	p, err := current(ctx, w.Policies)
	if err != nil {
		return sourcing.Policy{}, err
	}
	w.policyMetrics().PolicyVersionRead(ctx, p.Version())
	return sourcing.Policy{
		Version: p.Version(), CrossRegionSurchargeCents: int64(p.CrossRegionSurchargeCents()), MaxCostDeviationPercent: p.MaxCostDeviationPercent(),
	}, nil
}

// SourcingPolicies are the two use cases of /sourcing-policy.
type SourcingPolicies struct{ writer }

// NewSourcingPolicies builds them.
func NewSourcingPolicies(d Deps) *SourcingPolicies { return &SourcingPolicies{writer{d}} }

// Current returns the current policy: the published version with the highest
// number, or the defaults with version 0 when nothing was published.
func (uc *SourcingPolicies) Current(ctx context.Context) (views.SourcingPolicy, error) {
	p, err := current(ctx, uc.Policies)
	if err != nil {
		return views.SourcingPolicy{}, err
	}
	return views.SourcingPolicyFrom(p), nil
}

// Publish validates the input, stores version current+1 and appends
// SourcingPolicyPublished to the outbox in the same unit of work. A concurrent
// publish that took the same number is retried once on fresh state.
func (uc *SourcingPolicies) Publish(ctx context.Context, in policy.Input) (views.SourcingPolicy, error) {
	if uc.Policies == nil {
		return views.SourcingPolicy{}, errors.New("sourcing policy repository is not wired")
	}
	v, err := transact(ctx, uc.UoW, func(ctx context.Context) (views.SourcingPolicy, error) {
		cur, err := current(ctx, uc.Policies)
		if err != nil {
			return views.SourcingPolicy{}, err
		}
		p, events, err := policy.Publish(cur.Version(), in, uc.now())
		if err != nil {
			return views.SourcingPolicy{}, err
		}
		if err := uc.Policies.Save(ctx, p); err != nil {
			return views.SourcingPolicy{}, err
		}
		if err := uc.publish(ctx, events); err != nil {
			return views.SourcingPolicy{}, err
		}
		return views.SourcingPolicyFrom(p), nil
	})
	if err == nil {
		uc.policyMetrics().PolicyVersionRead(ctx, v.Version)
	}
	return v, err
}
