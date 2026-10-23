package memory

import (
	"context"
	"slices"
	"strings"

	"github.com/claudioed/order-sourcing/internal/application/ports"
	"github.com/claudioed/order-sourcing/internal/application/repository"
	"github.com/claudioed/order-sourcing/internal/domain/network"
	"github.com/claudioed/order-sourcing/internal/domain/sourcing"
)

// ReadModels is the in-memory ports.ReadModels.
type ReadModels struct{ s *Store }

func get[K comparable, V any](s *Store, ctx context.Context, m map[K]V, key K) (V, error) {
	defer s.enter(ctx)()
	v, ok := m[key]
	if !ok {
		return v, ports.ErrNotFound
	}
	return v, nil
}

func put[K comparable, V any](s *Store, ctx context.Context, m map[K]V, key K, v V) error {
	defer s.enter(ctx)()
	m[key] = v
	return nil
}

// GetNode implements ports.ReadModels.
func (r *ReadModels) GetNode(ctx context.Context, id string) (network.Node, error) {
	return get(r.s, ctx, r.s.nodes, id)
}

// PutNode implements ports.ReadModels.
func (r *ReadModels) PutNode(ctx context.Context, n network.Node) error {
	return put(r.s, ctx, r.s.nodes, n.ID, n)
}

// GetLane implements ports.ReadModels.
func (r *ReadModels) GetLane(ctx context.Context, id string) (network.Lane, error) {
	return get(r.s, ctx, r.s.lanes, id)
}

// PutLane implements ports.ReadModels.
func (r *ReadModels) PutLane(ctx context.Context, l network.Lane) error {
	return put(r.s, ctx, r.s.lanes, l.ID, l)
}

// GetDeparture implements ports.ReadModels.
func (r *ReadModels) GetDeparture(ctx context.Context, id string) (network.Departure, error) {
	return get(r.s, ctx, r.s.departures, id)
}

// PutDeparture implements ports.ReadModels.
func (r *ReadModels) PutDeparture(ctx context.Context, d network.Departure) error {
	return put(r.s, ctx, r.s.departures, d.ID, d)
}

// GetWave implements ports.ReadModels.
func (r *ReadModels) GetWave(ctx context.Context, id string) (network.Wave, error) {
	return get(r.s, ctx, r.s.waves, id)
}

// PutWave implements ports.ReadModels.
func (r *ReadModels) PutWave(ctx context.Context, w network.Wave) error {
	return put(r.s, ctx, r.s.waves, w.ID, w)
}

// GetServiceArea implements ports.ReadModels.
func (r *ReadModels) GetServiceArea(ctx context.Context, prefix string) (network.ServiceArea, error) {
	return get(r.s, ctx, r.s.areas, prefix)
}

// PutServiceArea implements ports.ReadModels.
func (r *ReadModels) PutServiceArea(ctx context.Context, a network.ServiceArea) error {
	return put(r.s, ctx, r.s.areas, a.PostalPrefix, a)
}

// PutRegion implements ports.ReadModels.
func (r *ReadModels) PutRegion(ctx context.Context, g network.Region) error {
	return put(r.s, ctx, r.s.regions, g.ID, g)
}

// GetListing implements ports.ReadModels.
func (r *ReadModels) GetListing(ctx context.Context, id string) (network.Listing, error) {
	return get(r.s, ctx, r.s.listings, id)
}

// PutListing implements ports.ReadModels.
func (r *ReadModels) PutListing(ctx context.Context, l network.Listing) error {
	return put(r.s, ctx, r.s.listings, l.ID, l)
}

// PutDiscontinued implements ports.ReadModels.
func (r *ReadModels) PutDiscontinued(ctx context.Context, catalogItemID string) error {
	return put(r.s, ctx, r.s.discontinued, catalogItemID, true)
}

// GetScorecard implements ports.ReadModels.
func (r *ReadModels) GetScorecard(ctx context.Context, nodeID, window string) (network.Scorecard, error) {
	return get(r.s, ctx, r.s.scorecards, scorecardKey{nodeID, window})
}

// PutScorecard implements ports.ReadModels.
func (r *ReadModels) PutScorecard(ctx context.Context, sc network.Scorecard) error {
	return put(r.s, ctx, r.s.scorecards, scorecardKey{sc.NodeID, sc.Window}, sc)
}

// PutPressure implements ports.ReadModels.
func (r *ReadModels) PutPressure(ctx context.Context, p network.Pressure) error {
	return put(r.s, ctx, r.s.pressure, p.BookID, p)
}

// WorldReader is the in-memory ports.WorldReader.
type WorldReader struct{ s *Store }

// Load implements ports.WorldReader: the slice of the read models one search
// needs (the contract every adapter keeps, see internal/adapters/outbound/repocontract).
func (r *WorldReader) Load(ctx context.Context, q repository.WorldQuery) (sourcing.World, error) {
	defer r.s.enter(ctx)()
	w := sourcing.World{
		Nodes: cloneMap(r.s.nodes), Lanes: cloneMap(r.s.lanes), Area: r.s.areas[q.DestinationPostalPrefix],
		Discontinued: map[string]bool{}, Scorecards: map[string]network.Scorecard{},
	}
	for _, d := range r.s.departures {
		if d.Scheduled && !d.CutoffAt.Before(q.Now) {
			w.Departures = append(w.Departures, d)
		}
	}
	for _, wv := range r.s.waves {
		if wv.Scheduled && wv.StationID == w.Area.StationID && !wv.InductCutoffAt.Before(q.Now) {
			w.Waves = append(w.Waves, wv)
		}
	}
	for _, l := range r.s.listings {
		if slices.Contains(q.CatalogItemIDs, l.CatalogItemID) {
			w.Listings = append(w.Listings, l)
		}
	}
	for _, item := range q.CatalogItemIDs {
		if r.s.discontinued[item] {
			w.Discontinued[item] = true
		}
	}
	for k, sc := range r.s.scorecards {
		if k.window == network.Window30d {
			w.Scorecards[k.node] = sc
		}
	}
	for _, p := range r.s.pressure {
		w.Pressure = append(w.Pressure, p)
	}
	sortWorld(&w)
	return w, nil
}

// sortWorld orders the slices so a snapshot is deterministic.
func sortWorld(w *sourcing.World) {
	slices.SortFunc(w.Departures, func(a, b network.Departure) int { return strings.Compare(a.ID, b.ID) })
	slices.SortFunc(w.Waves, func(a, b network.Wave) int { return strings.Compare(a.ID, b.ID) })
	slices.SortFunc(w.Listings, func(a, b network.Listing) int { return strings.Compare(a.ID, b.ID) })
	slices.SortFunc(w.Pressure, func(a, b network.Pressure) int { return strings.Compare(a.BookID, b.BookID) })
}
