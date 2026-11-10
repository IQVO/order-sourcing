package views

import (
	"time"

	"github.com/claudioed/order-sourcing/internal/domain/policy"
)

// SourcingPolicy is the read view of the sourcing policy (GET and PUT
// /sourcing-policy). Version 0 is the built-in default: PublishedAt is the zero
// time and every lever is 0 (ADR 0014).
type SourcingPolicy struct {
	Version                   int
	CrossRegionSurchargeCents int
	MaxCostDeviationPercent   int
	PublishedAt               time.Time
}

// SourcingPolicyFrom builds the view of a policy.
func SourcingPolicyFrom(p *policy.Policy) SourcingPolicy {
	return SourcingPolicy{
		Version: p.Version(), CrossRegionSurchargeCents: p.CrossRegionSurchargeCents(),
		MaxCostDeviationPercent: p.MaxCostDeviationPercent(), PublishedAt: p.PublishedAt(),
	}
}
