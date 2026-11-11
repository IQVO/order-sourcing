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
	locked_at, lock_reason, pickup_departure_id, tracking_numbers, shipped_at, delivered, delivered_at, cancel_reason,
	deliver_by_earliest, consolidation_key, consolidated_into, members`

// jsonb shapes (private: the domain structs carry no tags).

type lineJSON struct {
	LineNo        int    `json:"lineNo"`
	CatalogItemID string `json:"catalogItemId"`
	ListingID     string `json:"listingId,omitempty"`
	OwnerRef      string `json:"ownerRef,omitempty"`
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
	// The money fields and the sourcing policy stamp (ADR 0011, ADR 0014) are
	// optional: a row written before them decodes with zeros.
	EstimatedCostCents int64 `json:"estimatedCostCents,omitempty"`
	CostComplete       bool  `json:"costComplete,omitempty"`
	SurchargeCents     int64 `json:"surchargeCents,omitempty"`
	PolicyVersion      int   `json:"policyVersion,omitempty"`
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
	lines, candidate, releases, excluded, tracking, delivered, members []byte
}

func encode(s assignment.Snapshot) (encoded, error) {
	var e encoded
	var err error
	lines := make([]lineJSON, 0, len(s.Lines))
	for _, l := range s.Lines {
		lines = append(lines, lineJSON{l.LineNo, l.CatalogItemID, l.ListingID, l.OwnerRef, l.Quantity})
	}
	if e.lines, err = toJSON(lines); err != nil {
		return e, err
	}
	if s.Candidate != nil {
		c := s.Candidate
		if e.candidate, err = toJSON(candidateJSON{
			NodeID: c.NodeID, PickupDepartureID: c.PickupDepartureID, PickupCutoffAt: c.PickupCutoffAt.UTC(), Legs: append([]string{}, c.Legs...),
			DeliveryWaveID: c.DeliveryWaveID, ETA: c.ETA.UTC(), Cost: c.Cost, EstimatedCostCents: c.EstimatedCostCents, CostComplete: c.CostComplete,
			SurchargeCents: c.SurchargeCents, PolicyVersion: c.PolicyVersion,
		}); err != nil {
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
	if e.delivered, err = toJSON(dl); err != nil {
		return e, err
	}
	members := make([]string, 0, len(s.Members))
	for _, m := range s.Members {
		members = append(members, string(m))
	}
	e.members, err = toJSON(members)
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
		nullTime(s.ShippedAt), e.delivered, nullTime(s.DeliveredAt), s.CancelReason,
		nullTime(s.Promise.DeliverByEarliest), s.ConsolidationKey, string(s.ConsolidatedInto), e.members, a.CurrentNodeID(),
	}
	return inTx(ctx, r.pool, func(q querier) error {
		if s.Version == 1 {
			return insertAssignment(ctx, q, args)
		}
		tag, err := q.Exec(ctx, `UPDATE assignments SET order_id=$2, group_no=$3, destination_postal_prefix=$4, promise_id=$5, tier=$6,
			deliver_by_latest=$7, witness_node_id=$8, revision_no=$9, lines=$10, state=$11, candidate=$12, release_at=$13, releases=$14,
			excluded_nodes=$15, version=$16, queued_at=$17, updated_at=$18, locked_at=$19, lock_reason=$20, pickup_departure_id=$21,
			tracking_numbers=$22, shipped_at=$23, delivered=$24, delivered_at=$25, cancel_reason=$26, deliver_by_earliest=$27,
			consolidation_key=$28, consolidated_into=$29, members=$30, node_id=$31
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
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26,$27,$28,$29,$30,$31)`, args...)
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
		membersB                                                   []byte
		releaseAt, lockedAt, shippedAt, deliveredAt, earliest      *time.Time
		deliverBy, queuedAt, updatedAt                             time.Time
		into                                                       string
	)
	err := row.Scan(&id, &s.OrderID, &s.GroupNo, &s.DestinationPostalPrefix, &s.Promise.PromiseID, &tier, &deliverBy,
		&s.Promise.WitnessNodeID, &s.Promise.RevisionNo, &lines, &state, &candidate, &releaseAt, &releases, &excluded, &s.Version,
		&queuedAt, &updatedAt, &lockedAt, &s.LockReason, &s.PickupDepartureID, &tracking, &shippedAt, &deliveredB, &deliveredAt, &s.CancelReason,
		&earliest, &s.ConsolidationKey, &into, &membersB)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ports.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("scan assignment: %w", err)
	}
	s.ID, s.State = assignment.ID(id), assignment.State(state)
	s.Promise.Tier, s.Promise.DeliverByLatest = assignment.Tier(tier), deliverBy.UTC()
	s.Promise.DeliverByEarliest, s.ConsolidatedInto = orZero(earliest), assignment.ID(into)
	s.QueuedAt, s.UpdatedAt = queuedAt.UTC(), updatedAt.UTC()
	s.ReleaseAt, s.LockedAt, s.ShippedAt, s.DeliveredAt = orZero(releaseAt), orZero(lockedAt), orZero(shippedAt), orZero(deliveredAt)
	if err := decode(&s, lines, candidate, releases, excluded, tracking, deliveredB); err != nil {
		return nil, fmt.Errorf("decode assignment %s: %w", id, err)
	}
	if err := decodeMembers(&s, membersB); err != nil {
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
		s.Lines = append(s.Lines, assignment.Line{LineNo: l.LineNo, CatalogItemID: l.CatalogItemID, ListingID: l.ListingID, OwnerRef: l.OwnerRef, Quantity: l.Quantity})
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
		Legs: c.Legs, DeliveryWaveID: c.DeliveryWaveID, ETA: c.ETA.UTC(), Cost: c.Cost, EstimatedCostCents: c.EstimatedCostCents,
		CostComplete: c.CostComplete, SurchargeCents: c.SurchargeCents, PolicyVersion: c.PolicyVersion}
	return nil
}

// decodeMembers reads the members jsonb column: the follower ids of a carrier.
func decodeMembers(s *assignment.Snapshot, raw []byte) error {
	var ids []string
	if err := json.Unmarshal(raw, &ids); err != nil {
		return err
	}
	for _, id := range ids {
		s.Members = append(s.Members, assignment.ID(id))
	}
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
