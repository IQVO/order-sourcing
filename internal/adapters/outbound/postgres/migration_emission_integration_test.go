//go:build integration

package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/claudioed/order-sourcing/internal/adapters/outbound/postgres"
	"github.com/claudioed/order-sourcing/internal/domain/network"
	"github.com/claudioed/order-sourcing/internal/testing/pgtest"
)

// emissionMigration is the migration 0007 of ADR 0017.
const emissionMigration = 7

// TestMigration0007AddsTheEmissionColumns: rows written before 0007 read back
// as undeclared (0 grams, NULL declaredAt), the new columns store and round-trip
// through the read models, and 0007 goes down.
func TestMigration0007AddsTheEmissionColumns(t *testing.T) {
	url := pgtest.URL(t)
	if err := postgres.MigrateTo(url, emissionMigration-1); err != nil {
		t.Fatalf("down to %d: %v", emissionMigration-1, err)
	}
	run, rmOld, closeOld := openAt(t, url)
	if err := run(`INSERT INTO rm_lanes (lane_id, origin, destination, mode, closed) VALUES ('l-old', 'a', 'b', 'GROUND', false)`); err != nil {
		t.Fatalf("a lane before 0007: %v", err)
	}
	if err := run(`INSERT INTO rm_waves (wave_id, station_id, capacity) VALUES ('w-old', 's1', 10)`); err != nil {
		t.Fatalf("a wave before 0007: %v", err)
	}
	if err := run(`SELECT emission_per_unit_g FROM rm_lanes`); err == nil {
		t.Fatal("the lane column must not exist before 0007")
	}
	closeOld()
	if err := postgres.MigrateTo(url, emissionMigration); err != nil {
		t.Fatalf("up to %d: %v", emissionMigration, err)
	}
	pool, rm, closeNew := openAt(t, url)
	defer closeNew()
	l, err := rm.GetLane(context.Background(), "l-old")
	if err != nil || l.EmissionPerUnitG != 0 || l.EmissionDeclared() {
		t.Fatalf("old lane = %+v %v, want it emission-undeclared", l, err)
	}
	w, err := rm.GetWave(context.Background(), "w-old")
	if err != nil || w.EmissionPerStopG != 0 || w.EmissionDeclared() {
		t.Fatalf("old wave = %+v %v, want it emission-undeclared", w, err)
	}
	// The new columns store and round-trip beside the costs.
	at := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	l.EmissionPerUnitG, l.EmissionDeclaredAt = 540, at
	l.CostPerUnitCents, l.CostDeclaredAt = 320, at
	if err := rm.PutLane(context.Background(), l); err != nil {
		t.Fatalf("put lane: %v", err)
	}
	back, err := rm.GetLane(context.Background(), "l-old")
	if err != nil || back.EmissionPerUnitG != 540 || !back.EmissionDeclared() || back.CostPerUnitCents != 320 {
		t.Fatalf("round-trip lane = %+v %v", back, err)
	}
	if _, err := pool.Exec(context.Background(), `UPDATE rm_waves SET emission_per_stop_g = 300, emission_declared_at = now()`); err != nil {
		t.Fatalf("the new wave column stores: %v", err)
	}
	if err := rm.PutWave(context.Background(), network.Wave{ID: "w-old", StationID: "s1", Scheduled: true, Capacity: 10, Remaining: 10, RemainingKnown: true, EmissionPerStopG: 300, EmissionDeclaredAt: at}); err != nil {
		t.Fatalf("put wave: %v", err)
	}
	wBack, err := rm.GetWave(context.Background(), "w-old")
	if err != nil || wBack.EmissionPerStopG != 300 || !wBack.EmissionDeclared() {
		t.Fatalf("round-trip wave = %+v %v", wBack, err)
	}
	if err := postgres.MigrateTo(url, emissionMigration-1); err != nil {
		t.Fatalf("0007 down: %v", err)
	}
	if err := postgres.MigrateTo(url, latestMigration); err != nil {
		t.Fatalf("back up: %v", err)
	}
}
