package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/claudioed/order-sourcing/internal/application/ports"
	"github.com/claudioed/order-sourcing/internal/domain/network"
)

// ReadModels is the Postgres ports.ReadModels: the write side of the five local
// read models (tables rm_*). Every Put is an upsert by the row key.
type ReadModels struct{ pool *pgxpool.Pool }

// NewReadModels constructs the read-model store over pool.
func NewReadModels(pool *pgxpool.Pool) *ReadModels { return &ReadModels{pool: pool} }

var _ ports.ReadModels = (*ReadModels)(nil)

func miss(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ports.ErrNotFound
	}
	return err
}

func tp(t *time.Time) time.Time { return orZero(t) }

// GetNode implements ports.ReadModels.
func (r *ReadModels) GetNode(ctx context.Context, id string) (network.Node, error) {
	var n network.Node
	var at *time.Time
	err := queryFor(ctx, r.pool).QueryRow(ctx, `SELECT node_id, node_type, subtype, operator, region_id, status, status_at, profile_revision,
		handling_minutes, sort_dwell_minutes FROM rm_nodes WHERE node_id = $1`, id).
		Scan(&n.ID, &n.Type, &n.Subtype, &n.Operator, &n.RegionID, &n.Status, &at, &n.ProfileRevision, &n.HandlingMinutes, &n.SortDwellMinutes)
	n.StatusAt = tp(at)
	return n, miss(err)
}

// PutNode implements ports.ReadModels.
func (r *ReadModels) PutNode(ctx context.Context, n network.Node) error {
	_, err := queryFor(ctx, r.pool).Exec(ctx, `INSERT INTO rm_nodes (node_id, node_type, subtype, operator, region_id, status, status_at, profile_revision,
		handling_minutes, sort_dwell_minutes) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
		ON CONFLICT (node_id) DO UPDATE SET node_type=$2, subtype=$3, operator=$4, region_id=$5, status=$6, status_at=$7, profile_revision=$8,
		handling_minutes=$9, sort_dwell_minutes=$10`,
		n.ID, n.Type, n.Subtype, n.Operator, n.RegionID, n.Status, nullTime(n.StatusAt), n.ProfileRevision, n.HandlingMinutes, n.SortDwellMinutes)
	return wrap("put node", err)
}

func wrap(what string, err error) error {
	if err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}
	return nil
}

// GetLane implements ports.ReadModels.
func (r *ReadModels) GetLane(ctx context.Context, id string) (network.Lane, error) {
	var l network.Lane
	err := queryFor(ctx, r.pool).QueryRow(ctx, `SELECT lane_id, origin, destination, mode, closed FROM rm_lanes WHERE lane_id = $1`, id).
		Scan(&l.ID, &l.Origin, &l.Destination, &l.Mode, &l.Closed)
	return l, miss(err)
}

// PutLane implements ports.ReadModels.
func (r *ReadModels) PutLane(ctx context.Context, l network.Lane) error {
	_, err := queryFor(ctx, r.pool).Exec(ctx, `INSERT INTO rm_lanes (lane_id, origin, destination, mode, closed) VALUES ($1,$2,$3,$4,$5)
		ON CONFLICT (lane_id) DO UPDATE SET origin=$2, destination=$3, mode=$4, closed=$5`, l.ID, l.Origin, l.Destination, l.Mode, l.Closed)
	return wrap("put lane", err)
}

const departureColumns = `departure_id, lane_id, kind, origin, destination, cutoff_at, departs_at, arrives_at, capacity, remaining, remaining_known, scheduled, cancelled`

func scanDeparture(row pgx.Row) (network.Departure, error) {
	var d network.Departure
	var cutoff, departs, arrives *time.Time
	err := row.Scan(&d.ID, &d.LaneID, &d.Kind, &d.Origin, &d.Destination, &cutoff, &departs, &arrives, &d.Capacity, &d.Remaining,
		&d.RemainingKnown, &d.Scheduled, &d.Cancelled)
	d.CutoffAt, d.DepartsAt, d.ArrivesAt = tp(cutoff), tp(departs), tp(arrives)
	return d, err
}

// GetDeparture implements ports.ReadModels.
func (r *ReadModels) GetDeparture(ctx context.Context, id string) (network.Departure, error) {
	d, err := scanDeparture(queryFor(ctx, r.pool).QueryRow(ctx, `SELECT `+departureColumns+` FROM rm_departures WHERE departure_id = $1`, id))
	return d, miss(err)
}

// PutDeparture implements ports.ReadModels.
func (r *ReadModels) PutDeparture(ctx context.Context, d network.Departure) error {
	_, err := queryFor(ctx, r.pool).Exec(ctx, `INSERT INTO rm_departures (`+departureColumns+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)
		ON CONFLICT (departure_id) DO UPDATE SET lane_id=$2, kind=$3, origin=$4, destination=$5, cutoff_at=$6, departs_at=$7, arrives_at=$8,
		capacity=$9, remaining=$10, remaining_known=$11, scheduled=$12, cancelled=$13`,
		d.ID, d.LaneID, d.Kind, d.Origin, d.Destination, nullTime(d.CutoffAt), nullTime(d.DepartsAt), nullTime(d.ArrivesAt), d.Capacity, d.Remaining,
		d.RemainingKnown, d.Scheduled, d.Cancelled)
	return wrap("put departure", err)
}

const waveColumns = `wave_id, station_id, induct_cutoff_at, window_start, window_end, capacity, remaining, remaining_known, scheduled, cancelled`

func scanWave(row pgx.Row) (network.Wave, error) {
	var w network.Wave
	var induct, start, end *time.Time
	err := row.Scan(&w.ID, &w.StationID, &induct, &start, &end, &w.Capacity, &w.Remaining, &w.RemainingKnown, &w.Scheduled, &w.Cancelled)
	w.InductCutoffAt, w.WindowStart, w.WindowEnd = tp(induct), tp(start), tp(end)
	return w, err
}

// GetWave implements ports.ReadModels.
func (r *ReadModels) GetWave(ctx context.Context, id string) (network.Wave, error) {
	w, err := scanWave(queryFor(ctx, r.pool).QueryRow(ctx, `SELECT `+waveColumns+` FROM rm_waves WHERE wave_id = $1`, id))
	return w, miss(err)
}

// PutWave implements ports.ReadModels.
func (r *ReadModels) PutWave(ctx context.Context, w network.Wave) error {
	_, err := queryFor(ctx, r.pool).Exec(ctx, `INSERT INTO rm_waves (`+waveColumns+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
		ON CONFLICT (wave_id) DO UPDATE SET station_id=$2, induct_cutoff_at=$3, window_start=$4, window_end=$5, capacity=$6, remaining=$7,
		remaining_known=$8, scheduled=$9, cancelled=$10`,
		w.ID, w.StationID, nullTime(w.InductCutoffAt), nullTime(w.WindowStart), nullTime(w.WindowEnd), w.Capacity, w.Remaining, w.RemainingKnown, w.Scheduled, w.Cancelled)
	return wrap("put wave", err)
}

// GetServiceArea implements ports.ReadModels.
func (r *ReadModels) GetServiceArea(ctx context.Context, prefix string) (network.ServiceArea, error) {
	var s network.ServiceArea
	err := queryFor(ctx, r.pool).QueryRow(ctx, `SELECT postal_prefix, station_id, region_id, version FROM rm_service_areas WHERE postal_prefix = $1`, prefix).
		Scan(&s.PostalPrefix, &s.StationID, &s.RegionID, &s.Version)
	return s, miss(err)
}

// PutServiceArea implements ports.ReadModels.
func (r *ReadModels) PutServiceArea(ctx context.Context, s network.ServiceArea) error {
	_, err := queryFor(ctx, r.pool).Exec(ctx, `INSERT INTO rm_service_areas (postal_prefix, station_id, region_id, version) VALUES ($1,$2,$3,$4)
		ON CONFLICT (postal_prefix) DO UPDATE SET station_id=$2, region_id=$3, version=$4`, s.PostalPrefix, s.StationID, s.RegionID, s.Version)
	return wrap("put service area", err)
}

// PutRegion implements ports.ReadModels.
func (r *ReadModels) PutRegion(ctx context.Context, g network.Region) error {
	_, err := queryFor(ctx, r.pool).Exec(ctx, `INSERT INTO rm_regions (region_id, code, name) VALUES ($1,$2,$3)
		ON CONFLICT (region_id) DO UPDATE SET code=$2, name=$3`, g.ID, g.Code, g.Name)
	return wrap("put region", err)
}

// GetListing implements ports.ReadModels.
func (r *ReadModels) GetListing(ctx context.Context, id string) (network.Listing, error) {
	l, err := scanListing(queryFor(ctx, r.pool).QueryRow(ctx, `SELECT listing_id, catalog_item_id, node_id, available_qty, as_of, suppressed
		FROM rm_listings WHERE listing_id = $1`, id))
	return l, miss(err)
}

func scanListing(row pgx.Row) (network.Listing, error) {
	var l network.Listing
	var asOf *time.Time
	err := row.Scan(&l.ID, &l.CatalogItemID, &l.NodeID, &l.AvailableQty, &asOf, &l.Suppressed)
	l.AsOf = tp(asOf)
	return l, err
}

// PutListing implements ports.ReadModels.
func (r *ReadModels) PutListing(ctx context.Context, l network.Listing) error {
	_, err := queryFor(ctx, r.pool).Exec(ctx, `INSERT INTO rm_listings (listing_id, catalog_item_id, node_id, available_qty, as_of, suppressed)
		VALUES ($1,$2,$3,$4,$5,$6) ON CONFLICT (listing_id) DO UPDATE SET catalog_item_id=$2, node_id=$3, available_qty=$4, as_of=$5, suppressed=$6`,
		l.ID, l.CatalogItemID, l.NodeID, l.AvailableQty, nullTime(l.AsOf), l.Suppressed)
	return wrap("put listing", err)
}

// PutDiscontinued implements ports.ReadModels.
func (r *ReadModels) PutDiscontinued(ctx context.Context, catalogItemID string) error {
	_, err := queryFor(ctx, r.pool).Exec(ctx, `INSERT INTO rm_discontinued_items (catalog_item_id) VALUES ($1) ON CONFLICT DO NOTHING`, catalogItemID)
	return wrap("put discontinued item", err)
}

// GetScorecard implements ports.ReadModels.
func (r *ReadModels) GetScorecard(ctx context.Context, nodeID, window string) (network.Scorecard, error) {
	var s network.Scorecard
	var at *time.Time
	err := queryFor(ctx, r.pool).QueryRow(ctx, `SELECT node_id, score_window, grade, computed_at FROM rm_scorecards WHERE node_id = $1 AND score_window = $2`, nodeID, window).
		Scan(&s.NodeID, &s.Window, &s.Grade, &at)
	s.ComputedAt = tp(at)
	return s, miss(err)
}

// PutScorecard implements ports.ReadModels.
func (r *ReadModels) PutScorecard(ctx context.Context, s network.Scorecard) error {
	_, err := queryFor(ctx, r.pool).Exec(ctx, `INSERT INTO rm_scorecards (node_id, score_window, grade, computed_at) VALUES ($1,$2,$3,$4)
		ON CONFLICT (node_id, score_window) DO UPDATE SET grade=$3, computed_at=$4`, s.NodeID, s.Window, s.Grade, nullTime(s.ComputedAt))
	return wrap("put scorecard", err)
}

// PutPressure implements ports.ReadModels.
func (r *ReadModels) PutPressure(ctx context.Context, p network.Pressure) error {
	_, err := queryFor(ctx, r.pool).Exec(ctx, `INSERT INTO rm_book_pressure (book_id, node_id, pickup_departure_id) VALUES ($1,$2,$3)
		ON CONFLICT (book_id) DO UPDATE SET node_id=$2, pickup_departure_id=$3`, p.BookID, p.NodeID, p.PickupDepartureID)
	return wrap("put pressure", err)
}
