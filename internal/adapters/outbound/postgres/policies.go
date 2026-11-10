package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/claudioed/order-sourcing/internal/application/ports"
	"github.com/claudioed/order-sourcing/internal/domain/policy"
)

// PolicyRepository is the Postgres ports.PolicyRepository (table
// sourcing_policies, one jsonb snapshot per version, ADR 0014).
type PolicyRepository struct{ pool *pgxpool.Pool }

// NewPolicyRepository constructs the repository over pool.
func NewPolicyRepository(pool *pgxpool.Pool) *PolicyRepository { return &PolicyRepository{pool: pool} }

var _ ports.PolicyRepository = (*PolicyRepository)(nil)

// policyJSON is the stored snapshot (private: the domain structs carry no tags).
type policyJSON struct {
	Version                   int       `json:"version"`
	CrossRegionSurchargeCents int       `json:"crossRegionSurchargeCents"`
	MaxCostDeviationPercent   int       `json:"maxCostDeviationPercent"`
	PublishedAt               time.Time `json:"publishedAt"`
}

// Current returns the highest version or ports.ErrNotFound.
func (r *PolicyRepository) Current(ctx context.Context) (*policy.Policy, error) {
	var raw []byte
	err := queryFor(ctx, r.pool).QueryRow(ctx, `SELECT snapshot FROM sourcing_policies ORDER BY version DESC LIMIT 1`).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ports.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read sourcing policy: %w", err)
	}
	var j policyJSON
	if err := json.Unmarshal(raw, &j); err != nil {
		return nil, fmt.Errorf("decode sourcing policy: %w", err)
	}
	return policy.Restore(policy.Snapshot{
		Version: j.Version, CrossRegionSurchargeCents: j.CrossRegionSurchargeCents, MaxCostDeviationPercent: j.MaxCostDeviationPercent,
		PublishedAt: j.PublishedAt.UTC(),
	})
}

// Save stores a new version; an existing version is ports.ErrConcurrentUpdate.
func (r *PolicyRepository) Save(ctx context.Context, p *policy.Policy) error {
	s := p.Snapshot()
	raw, err := json.Marshal(policyJSON{
		Version: s.Version, CrossRegionSurchargeCents: s.CrossRegionSurchargeCents, MaxCostDeviationPercent: s.MaxCostDeviationPercent,
		PublishedAt: s.PublishedAt.UTC(),
	})
	if err != nil {
		return fmt.Errorf("encode sourcing policy: %w", err)
	}
	// ON CONFLICT DO NOTHING, not a unique violation: a lost race raises no
	// server error, so it leaves an enclosing transaction (the idempotency
	// middleware's) usable and the use case's retry can read the winner.
	tag, err := queryFor(ctx, r.pool).Exec(ctx, `INSERT INTO sourcing_policies (version, snapshot) VALUES ($1, $2) ON CONFLICT (version) DO NOTHING`, s.Version, raw)
	if err != nil {
		return fmt.Errorf("save sourcing policy %d: %w", s.Version, err)
	}
	if tag.RowsAffected() == 0 {
		return ports.ErrConcurrentUpdate
	}
	return nil
}
