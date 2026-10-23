package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/claudioed/order-sourcing/internal/application/ports"
)

// Inbox is the Postgres ports.Inbox (table processed_events).
type Inbox struct{ pool *pgxpool.Pool }

// NewInbox constructs the inbox over pool.
func NewInbox(pool *pgxpool.Pool) *Inbox { return &Inbox{pool: pool} }

var _ ports.Inbox = (*Inbox)(nil)

// Claim implements ports.Inbox: the insert of a new CloudEvents id wins once.
func (i *Inbox) Claim(ctx context.Context, eventID, eventType string) (bool, error) {
	tag, err := queryFor(ctx, i.pool).Exec(ctx, `INSERT INTO processed_events (event_id, event_type) VALUES ($1, $2) ON CONFLICT (event_id) DO NOTHING`, eventID, eventType)
	if err != nil {
		return false, fmt.Errorf("claim processed event %s: %w", eventID, err)
	}
	return tag.RowsAffected() == 1, nil
}
