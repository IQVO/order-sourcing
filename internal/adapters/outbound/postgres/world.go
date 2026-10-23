package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/claudioed/order-sourcing/internal/application/ports"
	"github.com/claudioed/order-sourcing/internal/application/repository"
	"github.com/claudioed/order-sourcing/internal/domain/network"
	"github.com/claudioed/order-sourcing/internal/domain/sourcing"
)

// WorldReader is the Postgres ports.WorldReader: it reads the slice of the read
// models one search needs, on the unit of work's transaction when there is one
// (the contract every adapter keeps, internal/adapters/outbound/repocontract).
type WorldReader struct{ pool *pgxpool.Pool }

// NewWorldReader constructs the world reader over pool.
func NewWorldReader(pool *pgxpool.Pool) *WorldReader { return &WorldReader{pool: pool} }

var _ ports.WorldReader = (*WorldReader)(nil)

// Load implements ports.WorldReader.
func (r *WorldReader) Load(ctx context.Context, q repository.WorldQuery) (sourcing.World, error) {
	db := queryFor(ctx, r.pool)
	now := q.Now.UTC()
	w := sourcing.World{Discontinued: map[string]bool{}, Scorecards: map[string]network.Scorecard{}}
	rm := &ReadModels{pool: r.pool}
	area, err := rm.GetServiceArea(ctx, q.DestinationPostalPrefix)
	if err != nil && !errors.Is(err, ports.ErrNotFound) {
		return w, err
	}
	w.Area = area
	loads := []func() error{
		func() (err error) { w.Nodes, err = loadNodes(ctx, db); return },
		func() (err error) { w.Lanes, err = loadLanes(ctx, db); return },
		func() (err error) { w.Departures, err = loadDepartures(ctx, db, now); return },
		func() (err error) { w.Waves, err = loadWaves(ctx, db, w.Area.StationID, now); return },
		func() (err error) { w.Listings, err = loadListings(ctx, db, q.CatalogItemIDs); return },
		func() (err error) { w.Discontinued, err = loadDiscontinued(ctx, db, q.CatalogItemIDs); return },
		func() (err error) { w.Scorecards, err = loadScorecards(ctx, db); return },
		func() (err error) { w.Pressure, err = loadPressure(ctx, db); return },
	}
	for _, load := range loads {
		if err := load(); err != nil {
			return sourcing.World{}, fmt.Errorf("load world: %w", err)
		}
	}
	return w, nil
}

// collect runs a query and scans every row with scan.
func collect[T any](ctx context.Context, db querier, sql string, scan func(pgx.Rows) (T, error), args ...any) ([]T, error) {
	rows, err := db.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []T
	for rows.Next() {
		v, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func loadNodes(ctx context.Context, db querier) (map[string]network.Node, error) {
	rows, err := collect(ctx, db, `SELECT node_id, node_type, subtype, operator, region_id, status, status_at, profile_revision,
		handling_minutes, sort_dwell_minutes FROM rm_nodes`, func(r pgx.Rows) (network.Node, error) {
		var n network.Node
		var at *time.Time
		err := r.Scan(&n.ID, &n.Type, &n.Subtype, &n.Operator, &n.RegionID, &n.Status, &at, &n.ProfileRevision, &n.HandlingMinutes, &n.SortDwellMinutes)
		n.StatusAt = tp(at)
		return n, err
	})
	out := make(map[string]network.Node, len(rows))
	for _, n := range rows {
		out[n.ID] = n
	}
	return out, err
}

func loadLanes(ctx context.Context, db querier) (map[string]network.Lane, error) {
	rows, err := collect(ctx, db, `SELECT lane_id, origin, destination, mode, closed FROM rm_lanes`, func(r pgx.Rows) (network.Lane, error) {
		var l network.Lane
		err := r.Scan(&l.ID, &l.Origin, &l.Destination, &l.Mode, &l.Closed)
		return l, err
	})
	out := make(map[string]network.Lane, len(rows))
	for _, l := range rows {
		out[l.ID] = l
	}
	return out, err
}

func loadDepartures(ctx context.Context, db querier, now time.Time) ([]network.Departure, error) {
	return collect(ctx, db, `SELECT `+departureColumns+` FROM rm_departures WHERE scheduled AND cutoff_at >= $1 ORDER BY departure_id`,
		func(r pgx.Rows) (network.Departure, error) { return scanDeparture(r) }, now)
}

func loadWaves(ctx context.Context, db querier, station string, now time.Time) ([]network.Wave, error) {
	return collect(ctx, db, `SELECT `+waveColumns+` FROM rm_waves WHERE scheduled AND station_id = $1 AND induct_cutoff_at >= $2 ORDER BY wave_id`,
		func(r pgx.Rows) (network.Wave, error) { return scanWave(r) }, station, now)
}

func loadListings(ctx context.Context, db querier, items []string) ([]network.Listing, error) {
	return collect(ctx, db, `SELECT listing_id, catalog_item_id, node_id, available_qty, as_of, suppressed FROM rm_listings
		WHERE catalog_item_id = ANY($1) ORDER BY listing_id`, func(r pgx.Rows) (network.Listing, error) { return scanListing(r) }, items)
}

func loadDiscontinued(ctx context.Context, db querier, items []string) (map[string]bool, error) {
	rows, err := collect(ctx, db, `SELECT catalog_item_id FROM rm_discontinued_items WHERE catalog_item_id = ANY($1)`,
		func(r pgx.Rows) (string, error) { var id string; return id, r.Scan(&id) }, items)
	out := map[string]bool{}
	for _, id := range rows {
		out[id] = true
	}
	return out, err
}

func loadScorecards(ctx context.Context, db querier) (map[string]network.Scorecard, error) {
	rows, err := collect(ctx, db, `SELECT node_id, score_window, grade, computed_at FROM rm_scorecards WHERE score_window = $1`,
		func(r pgx.Rows) (network.Scorecard, error) {
			var s network.Scorecard
			var at *time.Time
			err := r.Scan(&s.NodeID, &s.Window, &s.Grade, &at)
			s.ComputedAt = tp(at)
			return s, err
		}, network.Window30d)
	out := map[string]network.Scorecard{}
	for _, s := range rows {
		out[s.NodeID] = s
	}
	return out, err
}

func loadPressure(ctx context.Context, db querier) ([]network.Pressure, error) {
	return collect(ctx, db, `SELECT book_id, node_id, pickup_departure_id FROM rm_book_pressure ORDER BY book_id`,
		func(r pgx.Rows) (network.Pressure, error) {
			var p network.Pressure
			return p, r.Scan(&p.BookID, &p.NodeID, &p.PickupDepartureID)
		})
}
