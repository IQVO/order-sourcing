package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/claudioed/order-sourcing/internal/application/ports"
	"github.com/claudioed/order-sourcing/internal/application/repository"
	"github.com/claudioed/order-sourcing/internal/domain/assignment"
)

// AssignmentRepository is the Postgres ports.AssignmentRepository. The nested
// parts of the aggregate (lines, candidate, releases, exclusions, delivered
// packages) are jsonb columns of the one row, so a save is one statement.
type AssignmentRepository struct{ pool *pgxpool.Pool }

// NewAssignmentRepository constructs the repository over pool.
func NewAssignmentRepository(pool *pgxpool.Pool) *AssignmentRepository {
	return &AssignmentRepository{pool: pool}
}

var _ ports.AssignmentRepository = (*AssignmentRepository)(nil)

const assignmentColumns = `assignment_id, order_id, group_no, destination_postal_prefix, promise_id, tier, deliver_by_latest,
	witness_node_id, revision_no, lines, state, candidate, release_at, releases, excluded_nodes, version, queued_at, updated_at,
	locked_at, lock_reason, pickup_departure_id, tracking_numbers, shipped_at, delivered, delivered_at, cancel_reason`

// jsonb shapes (private: the domain structs carry no tags).

type lineJSON struct {
	LineNo        int    `json:"lineNo"`
	CatalogItemID string `json:"catalogItemId"`
	ListingID     string `json:"listingId,omitempty"`
	Quantity      int    `json:"quantity"`
}

type candidateJSON struct {
	NodeID            string    `json:"nodeId"`
	PickupDepartureID string    `json:"pickupDepartureId"`
	PickupCutoffAt    time.Time `json:"pickupCutoffAt"`
	Legs              []string  `json:"legs"`
	DeliveryWaveID    string    `json:"deliveryWaveId"`
	ETA               time.Time `json:"eta"`
	Cost              int64     `json:"cost"`
}

type releaseJSON struct {
	No             int       `json:"no"`
	NodeID         string    `json:"nodeId"`
	ReleasedAt     time.Time `json:"releasedAt"`
	RequiredShipBy time.Time `json:"requiredShipBy"`
	Outcome        string    `json:"outcome"`
	ReasonCode     string    `json:"reasonCode,omitempty"`
}

type excludedJSON struct {
	NodeID string `json:"nodeId"`
	Reason string `json:"reason"`
}

type deliveredJSON struct {
	TrackingNumber string    `json:"trackingNumber"`
	DeliveredAt    time.Time `json:"deliveredAt"`
}

func toJSON(v any) ([]byte, error) { return json.Marshal(v) }

func nullTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UTC()
}

func orZero(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return t.UTC()
}

// encoded is the jsonb columns of one snapshot.
type encoded struct {
	lines, candidate, releases, excluded, tracking, delivered []byte
}

func encode(s assignment.Snapshot) (encoded, error) {
	var e encoded
	var err error
	lines := make([]lineJSON, 0, len(s.Lines))
	for _, l := range s.Lines {
		lines = append(lines, lineJSON{l.LineNo, l.CatalogItemID, l.ListingID, l.Quantity})
	}
	if e.lines, err = toJSON(lines); err != nil {
		return e, err
	}
	if s.Candidate != nil {
		c := s.Candidate
		if e.candidate, err = toJSON(candidateJSON{c.NodeID, c.PickupDepartureID, c.PickupCutoffAt.UTC(), append([]string{}, c.Legs...), c.DeliveryWaveID, c.ETA.UTC(), c.Cost}); err != nil {
			return e, err
		}
	}
	rels := make([]releaseJSON, 0, len(s.Releases))
	for _, r := range s.Releases {
		rels = append(rels, releaseJSON{r.No, r.NodeID, r.ReleasedAt.UTC(), r.RequiredShipBy.UTC(), string(r.Outcome), r.ReasonCode})
	}
	if e.releases, err = toJSON(rels); err != nil {
		return e, err
	}
	ex := make([]excludedJSON, 0, len(s.ExcludedNodes))
	for _, n := range s.ExcludedNodes {
		ex = append(ex, excludedJSON{n.NodeID, n.Reason})
	}
	if e.excluded, err = toJSON(ex); err != nil {
		return e, err
	}
	if e.tracking, err = toJSON(append([]string{}, s.TrackingNumbers...)); err != nil {
		return e, err
	}
	dl := make([]deliveredJSON, 0, len(s.Delivered))
	for _, d := range s.Delivered {
		dl = append(dl, deliveredJSON{d.TrackingNumber, d.DeliveredAt.UTC()})
	}
	e.delivered, err = toJSON(dl)
	return e, err
}

// Save implements ports.AssignmentRepository.
func (r *AssignmentRepository) Save(ctx context.Context, a *assignment.Assignment) error {
	s := a.Snapshot()
	e, err := encode(s)
	if err != nil {
		return fmt.Errorf("encode assignment %s: %w", s.ID, err)
	}
	args := []any{
		string(s.ID), s.OrderID, s.GroupNo, s.DestinationPostalPrefix, s.Promise.PromiseID, string(s.Promise.Tier), s.Promise.DeliverByLatest.UTC(),
		s.Promise.WitnessNodeID, s.Promise.RevisionNo, e.lines, string(s.State), nullBytes(e.candidate), nullTime(s.ReleaseAt), e.releases, e.excluded,
		s.Version, s.QueuedAt.UTC(), s.UpdatedAt.UTC(), nullTime(s.LockedAt), s.LockReason, s.PickupDepartureID, e.tracking,
		nullTime(s.ShippedAt), e.delivered, nullTime(s.DeliveredAt), s.CancelReason, a.CurrentNodeID(),
	}
	return inTx(ctx, r.pool, func(q querier) error {
		if s.Version == 1 {
			return insertAssignment(ctx, q, args)
		}
		tag, err := q.Exec(ctx, `UPDATE assignments SET order_id=$2, group_no=$3, destination_postal_prefix=$4, promise_id=$5, tier=$6,
			deliver_by_latest=$7, witness_node_id=$8, revision_no=$9, lines=$10, state=$11, candidate=$12, release_at=$13, releases=$14,
			excluded_nodes=$15, version=$16, queued_at=$17, updated_at=$18, locked_at=$19, lock_reason=$20, pickup_departure_id=$21,
			tracking_numbers=$22, shipped_at=$23, delivered=$24, delivered_at=$25, cancel_reason=$26, node_id=$27
			WHERE assignment_id=$1 AND version=$16-1`, args...)
		if err != nil {
			return fmt.Errorf("update assignment %s: %w", s.ID, err)
		}
		if tag.RowsAffected() != 1 {
			return ports.ErrConcurrentUpdate
		}
		return nil
	})
}

func nullBytes(b []byte) any {
	if b == nil {
		return nil
	}
	return b
}

func insertAssignment(ctx context.Context, q querier, args []any) error {
	_, err := q.Exec(ctx, `INSERT INTO assignments (`+assignmentColumns+`, node_id)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26,$27)`, args...)
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg.Code == "23505" {
		if pg.ConstraintName == "assignments_order_group_key" {
			return ports.ErrDuplicateGroup
		}
		return ports.ErrConcurrentUpdate
	}
	if err != nil {
		return fmt.Errorf("insert assignment: %w", err)
	}
	return nil
}

func scanAssignment(row pgx.Row) (*assignment.Assignment, error) {
	var (
		s                                                          assignment.Snapshot
		id, tier, state                                            string
		lines, candidate, releases, excluded, tracking, deliveredB []byte
		releaseAt, lockedAt, shippedAt, deliveredAt                *time.Time
		deliverBy, queuedAt, updatedAt                             time.Time
	)
	err := row.Scan(&id, &s.OrderID, &s.GroupNo, &s.DestinationPostalPrefix, &s.Promise.PromiseID, &tier, &deliverBy,
		&s.Promise.WitnessNodeID, &s.Promise.RevisionNo, &lines, &state, &candidate, &releaseAt, &releases, &excluded, &s.Version,
		&queuedAt, &updatedAt, &lockedAt, &s.LockReason, &s.PickupDepartureID, &tracking, &shippedAt, &deliveredB, &deliveredAt, &s.CancelReason)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ports.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("scan assignment: %w", err)
	}
	s.ID, s.State = assignment.ID(id), assignment.State(state)
	s.Promise.Tier, s.Promise.DeliverByLatest = assignment.Tier(tier), deliverBy.UTC()
	s.QueuedAt, s.UpdatedAt = queuedAt.UTC(), updatedAt.UTC()
	s.ReleaseAt, s.LockedAt, s.ShippedAt, s.DeliveredAt = orZero(releaseAt), orZero(lockedAt), orZero(shippedAt), orZero(deliveredAt)
	if err := decode(&s, lines, candidate, releases, excluded, tracking, deliveredB); err != nil {
		return nil, fmt.Errorf("decode assignment %s: %w", id, err)
	}
	return assignment.Restore(s)
}

func decode(s *assignment.Snapshot, lines, candidate, releases, excluded, tracking, delivered []byte) error {
	var ls []lineJSON
	var rs []releaseJSON
	var ex []excludedJSON
	var ds []deliveredJSON
	for _, p := range []struct {
		raw []byte
		to  any
	}{{lines, &ls}, {releases, &rs}, {excluded, &ex}, {tracking, &s.TrackingNumbers}, {delivered, &ds}} {
		if err := json.Unmarshal(p.raw, p.to); err != nil {
			return err
		}
	}
	for _, l := range ls {
		s.Lines = append(s.Lines, assignment.Line{LineNo: l.LineNo, CatalogItemID: l.CatalogItemID, ListingID: l.ListingID, Quantity: l.Quantity})
	}
	for _, r := range rs {
		s.Releases = append(s.Releases, assignment.Release{No: r.No, NodeID: r.NodeID, ReleasedAt: r.ReleasedAt.UTC(), RequiredShipBy: r.RequiredShipBy.UTC(),
			Outcome: assignment.Outcome(r.Outcome), ReasonCode: r.ReasonCode})
	}
	for _, n := range ex {
		s.ExcludedNodes = append(s.ExcludedNodes, assignment.ExcludedNode{NodeID: n.NodeID, Reason: n.Reason})
	}
	for _, d := range ds {
		s.Delivered = append(s.Delivered, assignment.DeliveredPackage{TrackingNumber: d.TrackingNumber, DeliveredAt: d.DeliveredAt.UTC()})
	}
	if len(s.TrackingNumbers) == 0 {
		s.TrackingNumbers = nil
	}
	if candidate == nil {
		return nil
	}
	var c candidateJSON
	if err := json.Unmarshal(candidate, &c); err != nil {
		return err
	}
	s.Candidate = &assignment.Candidate{NodeID: c.NodeID, PickupDepartureID: c.PickupDepartureID, PickupCutoffAt: c.PickupCutoffAt.UTC(),
		Legs: c.Legs, DeliveryWaveID: c.DeliveryWaveID, ETA: c.ETA.UTC(), Cost: c.Cost}
	return nil
}

func (r *AssignmentRepository) one(ctx context.Context, where string, args ...any) (*assignment.Assignment, error) {
	return scanAssignment(queryFor(ctx, r.pool).QueryRow(ctx, `SELECT `+assignmentColumns+` FROM assignments WHERE `+where+` LIMIT 1`, args...))
}

// Get implements ports.AssignmentRepository.
func (r *AssignmentRepository) Get(ctx context.Context, id assignment.ID) (*assignment.Assignment, error) {
	return r.one(ctx, `assignment_id = $1`, string(id))
}

// FindByGroup implements ports.AssignmentRepository.
func (r *AssignmentRepository) FindByGroup(ctx context.Context, orderID string, groupNo int) (*assignment.Assignment, error) {
	return r.one(ctx, `order_id = $1 AND group_no = $2`, orderID, groupNo)
}

// FindByPromise implements ports.AssignmentRepository.
func (r *AssignmentRepository) FindByPromise(ctx context.Context, promiseID string) (*assignment.Assignment, error) {
	return r.one(ctx, `promise_id = $1 ORDER BY assignment_id`, promiseID)
}

// List implements ports.AssignmentRepository.
func (r *AssignmentRepository) List(ctx context.Context, f repository.AssignmentFilter) ([]*assignment.Assignment, error) {
	limit := f.Limit
	if limit <= 0 || limit > repository.FetchLimit {
		limit = repository.FetchLimit
	}
	rows, err := queryFor(ctx, r.pool).Query(ctx, `SELECT `+assignmentColumns+` FROM assignments
		WHERE ($1 = '' OR state = $1) AND ($2 = '' OR node_id = $2) AND ($3 = '' OR order_id = $3)
		AND assignment_id > $4
		ORDER BY assignment_id LIMIT $5`, string(f.State), f.NodeID, f.OrderID, f.After, limit)
	if err != nil {
		return nil, fmt.Errorf("list assignments: %w", err)
	}
	defer rows.Close()
	out := []*assignment.Assignment{}
	for rows.Next() {
		a, err := scanAssignment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (r *AssignmentRepository) ids(ctx context.Context, sql string, args ...any) ([]assignment.ID, error) {
	rows, err := queryFor(ctx, r.pool).Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("list assignment ids: %w", err)
	}
	defer rows.Close()
	out := []assignment.ID{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, assignment.ID(id))
	}
	return out, rows.Err()
}

// Due implements ports.AssignmentRepository.
func (r *AssignmentRepository) Due(ctx context.Context, now time.Time, limit int) ([]assignment.ID, error) {
	return r.ids(ctx, `SELECT assignment_id FROM assignments WHERE state = 'QUEUED' AND release_at <= $1
		ORDER BY release_at, assignment_id LIMIT $2`, now.UTC(), limit)
}

// Queued implements ports.AssignmentRepository.
func (r *AssignmentRepository) Queued(ctx context.Context, after assignment.ID, limit int) ([]assignment.ID, error) {
	return r.ids(ctx, `SELECT assignment_id FROM assignments WHERE state = 'QUEUED' AND assignment_id > $1
		ORDER BY assignment_id LIMIT $2`, string(after), limit)
}
