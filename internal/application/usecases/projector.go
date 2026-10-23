package usecases

import (
	"context"
	"time"

	"github.com/claudioed/order-sourcing/internal/domain/network"
)

// Projector applies sibling events to the five local read models. Each method
// is one unit of work: the inbox claim and the row change commit together, so a
// redelivered CloudEvents id changes nothing. The rules (newer revision wins, an
// older instant is ignored, any order of arrival) live in internal/domain/network.
type Projector struct{ writer }

// NewProjector builds the projector.
func NewProjector(d Deps) *Projector { return &Projector{writer{d}} }

// project runs one claimed read-model change.
func (p *Projector) project(ctx context.Context, m Meta, change func(ctx context.Context) error) error {
	_, err := transact(ctx, p.UoW, func(ctx context.Context) (struct{}, error) {
		if done, err := p.claim(ctx, m); err != nil || done {
			return struct{}{}, err
		}
		return struct{}{}, change(ctx)
	})
	return err
}

// upsert reads the row (a miss is the zero row, keyed by key), lets apply
// change it and stores it when apply reports a change.
func upsert[T any](ctx context.Context, get func(context.Context) (T, error), put func(context.Context, T) error, apply func(*T) bool) error {
	row, err := get(ctx)
	if _, err := found(err); err != nil {
		return err
	}
	if !apply(&row) {
		return nil
	}
	return put(ctx, row)
}

// NodeRegistered applies NodeRegistered.
func (p *Projector) NodeRegistered(ctx context.Context, m Meta, nodeID string, r network.Registered) error {
	return p.project(ctx, m, func(ctx context.Context) error {
		return upsert(ctx, func(ctx context.Context) (network.Node, error) { return p.ReadModels.GetNode(ctx, nodeID) }, p.ReadModels.PutNode,
			func(n *network.Node) bool { n.ID = nodeID; n.ApplyRegistered(r); return true })
	})
}

// NodeProfileDeclared applies NodeOperationalProfileDeclared.
func (p *Projector) NodeProfileDeclared(ctx context.Context, m Meta, nodeID string, revision, handlingMinutes, sortDwellMinutes int) error {
	return p.project(ctx, m, func(ctx context.Context) error {
		return upsert(ctx, func(ctx context.Context) (network.Node, error) { return p.ReadModels.GetNode(ctx, nodeID) }, p.ReadModels.PutNode,
			func(n *network.Node) bool {
				n.ID = nodeID
				return n.ApplyProfile(revision, handlingMinutes, sortDwellMinutes)
			})
	})
}

// NodeStatusChanged applies NodeActivated, NodeSuspended and NodeRetired.
func (p *Projector) NodeStatusChanged(ctx context.Context, m Meta, nodeID, status string, at time.Time) error {
	return p.project(ctx, m, func(ctx context.Context) error {
		return upsert(ctx, func(ctx context.Context) (network.Node, error) { return p.ReadModels.GetNode(ctx, nodeID) }, p.ReadModels.PutNode,
			func(n *network.Node) bool { n.ID = nodeID; return n.ApplyStatus(status, at) })
	})
}

// LaneOpened applies LaneOpened.
func (p *Projector) LaneOpened(ctx context.Context, m Meta, l network.Lane) error {
	return p.project(ctx, m, func(ctx context.Context) error {
		return upsert(ctx, func(ctx context.Context) (network.Lane, error) { return p.ReadModels.GetLane(ctx, l.ID) }, p.ReadModels.PutLane,
			func(row *network.Lane) bool { closed := row.Closed; *row = l; row.Closed = closed; return true })
	})
}

// LaneClosed applies LaneClosed.
func (p *Projector) LaneClosed(ctx context.Context, m Meta, laneID string) error {
	return p.project(ctx, m, func(ctx context.Context) error {
		return upsert(ctx, func(ctx context.Context) (network.Lane, error) { return p.ReadModels.GetLane(ctx, laneID) }, p.ReadModels.PutLane,
			func(row *network.Lane) bool { row.ID, row.Closed = laneID, true; return true })
	})
}

// ServiceAreaAssigned applies ServiceAreaAssigned; the newer assignmentVersion wins.
func (p *Projector) ServiceAreaAssigned(ctx context.Context, m Meta, s network.ServiceArea) error {
	return p.project(ctx, m, func(ctx context.Context) error {
		return upsert(ctx, func(ctx context.Context) (network.ServiceArea, error) {
			return p.ReadModels.GetServiceArea(ctx, s.PostalPrefix)
		},
			p.ReadModels.PutServiceArea, func(row *network.ServiceArea) bool {
				if row.PostalPrefix != "" && !s.Newer(*row) {
					return false
				}
				*row = s
				return true
			})
	})
}

// RegionDefined applies RegionDefined.
func (p *Projector) RegionDefined(ctx context.Context, m Meta, r network.Region) error {
	return p.project(ctx, m, func(ctx context.Context) error { return p.ReadModels.PutRegion(ctx, r) })
}

// DepartureScheduled applies DepartureScheduled.
func (p *Projector) DepartureScheduled(ctx context.Context, m Meta, d network.Departure) error {
	return p.project(ctx, m, func(ctx context.Context) error {
		return upsert(ctx, func(ctx context.Context) (network.Departure, error) { return p.ReadModels.GetDeparture(ctx, d.ID) },
			p.ReadModels.PutDeparture, func(row *network.Departure) bool { row.ApplySchedule(d); return true })
	})
}

// DepartureCapacityChanged applies DepartureCapacityChanged.
func (p *Projector) DepartureCapacityChanged(ctx context.Context, m Meta, departureID string, remaining int) error {
	return p.project(ctx, m, func(ctx context.Context) error {
		return upsert(ctx, func(ctx context.Context) (network.Departure, error) {
			return p.ReadModels.GetDeparture(ctx, departureID)
		},
			p.ReadModels.PutDeparture, func(row *network.Departure) bool { row.ID = departureID; row.ApplyCapacity(remaining); return true })
	})
}

// DepartureCancelled applies DepartureCancelled.
func (p *Projector) DepartureCancelled(ctx context.Context, m Meta, departureID string) error {
	return p.project(ctx, m, func(ctx context.Context) error {
		return upsert(ctx, func(ctx context.Context) (network.Departure, error) {
			return p.ReadModels.GetDeparture(ctx, departureID)
		},
			p.ReadModels.PutDeparture, func(row *network.Departure) bool { row.ID, row.Cancelled = departureID, true; return true })
	})
}

// WaveScheduled applies DeliveryWaveScheduled.
func (p *Projector) WaveScheduled(ctx context.Context, m Meta, w network.Wave) error {
	return p.project(ctx, m, func(ctx context.Context) error {
		return upsert(ctx, func(ctx context.Context) (network.Wave, error) { return p.ReadModels.GetWave(ctx, w.ID) },
			p.ReadModels.PutWave, func(row *network.Wave) bool { row.ApplySchedule(w); return true })
	})
}

// WaveCapacityChanged applies DeliveryWaveCapacityChanged.
func (p *Projector) WaveCapacityChanged(ctx context.Context, m Meta, waveID string, remaining int) error {
	return p.project(ctx, m, func(ctx context.Context) error {
		return upsert(ctx, func(ctx context.Context) (network.Wave, error) { return p.ReadModels.GetWave(ctx, waveID) },
			p.ReadModels.PutWave, func(row *network.Wave) bool { row.ID = waveID; row.ApplyCapacity(remaining); return true })
	})
}

// WaveCancelled applies DeliveryWaveCancelled (G5).
func (p *Projector) WaveCancelled(ctx context.Context, m Meta, waveID string) error {
	return p.project(ctx, m, func(ctx context.Context) error {
		return upsert(ctx, func(ctx context.Context) (network.Wave, error) { return p.ReadModels.GetWave(ctx, waveID) },
			p.ReadModels.PutWave, func(row *network.Wave) bool { row.ID, row.Cancelled = waveID, true; return true })
	})
}

// ListingCreated applies ListingCreated; a known listing keeps its quantity.
func (p *Projector) ListingCreated(ctx context.Context, m Meta, listingID, catalogItemID, nodeID string) error {
	return p.project(ctx, m, func(ctx context.Context) error {
		return upsert(ctx, func(ctx context.Context) (network.Listing, error) { return p.ReadModels.GetListing(ctx, listingID) },
			p.ReadModels.PutListing, func(row *network.Listing) bool {
				row.ID, row.CatalogItemID, row.NodeID = listingID, catalogItemID, nodeID
				return true
			})
	})
}

// ListingAvailabilityChanged applies ListingAvailabilityChanged; an older asOf is ignored.
func (p *Projector) ListingAvailabilityChanged(ctx context.Context, m Meta, listingID, catalogItemID, nodeID string, qty int, asOf time.Time) error {
	return p.project(ctx, m, func(ctx context.Context) error {
		return upsert(ctx, func(ctx context.Context) (network.Listing, error) { return p.ReadModels.GetListing(ctx, listingID) },
			p.ReadModels.PutListing, func(row *network.Listing) bool {
				row.ID = listingID
				if catalogItemID != "" {
					row.CatalogItemID = catalogItemID
				}
				if nodeID != "" {
					row.NodeID = nodeID
				}
				return row.ApplyAvailability(qty, asOf)
			})
	})
}

// ListingSuppressionChanged applies ListingSuppressed (true) and ListingRestored (false).
func (p *Projector) ListingSuppressionChanged(ctx context.Context, m Meta, listingID string, suppressed bool) error {
	return p.project(ctx, m, func(ctx context.Context) error {
		return upsert(ctx, func(ctx context.Context) (network.Listing, error) { return p.ReadModels.GetListing(ctx, listingID) },
			p.ReadModels.PutListing, func(row *network.Listing) bool { row.ID, row.Suppressed = listingID, suppressed; return true })
	})
}

// CatalogItemDiscontinued applies CatalogItemDiscontinued.
func (p *Projector) CatalogItemDiscontinued(ctx context.Context, m Meta, catalogItemID string) error {
	return p.project(ctx, m, func(ctx context.Context) error { return p.ReadModels.PutDiscontinued(ctx, catalogItemID) })
}

// ScorecardUpdated applies NodeScorecardUpdated; an older computedAt is ignored.
func (p *Projector) ScorecardUpdated(ctx context.Context, m Meta, s network.Scorecard) error {
	return p.project(ctx, m, func(ctx context.Context) error {
		return upsert(ctx, func(ctx context.Context) (network.Scorecard, error) {
			return p.ReadModels.GetScorecard(ctx, s.NodeID, s.Window)
		},
			p.ReadModels.PutScorecard, func(row *network.Scorecard) bool {
				if row.NodeID != "" && !s.Newer(*row) {
					return false
				}
				*row = s
				return true
			})
	})
}

// BookExhausted applies PromiseBookExhausted.
func (p *Projector) BookExhausted(ctx context.Context, m Meta, pr network.Pressure) error {
	return p.project(ctx, m, func(ctx context.Context) error { return p.ReadModels.PutPressure(ctx, pr) })
}
