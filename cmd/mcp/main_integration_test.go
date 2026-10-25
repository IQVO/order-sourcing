//go:build integration

package main

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	// The pool below is a pgx pool: this fleet's rule (and cmd/api's own
	// integration test) requires the testcontainers postgres import alongside
	// it in this package; the database itself comes from internal/testing/pgtest.
	_ "github.com/testcontainers/testcontainers-go/modules/postgres"

	inboundmcp "github.com/claudioed/order-sourcing/internal/adapters/inbound/mcp"
	"github.com/claudioed/order-sourcing/internal/adapters/outbound/postgres"
	"github.com/claudioed/order-sourcing/internal/domain/assignment"
	"github.com/claudioed/order-sourcing/internal/testing/pgtest"
)

func TestMain(m *testing.M) {
	code := m.Run()
	pgtest.Shutdown()
	os.Exit(code)
}

// seedNow is the fixture's fixed "now"; hours is seedNow plus n hours.
var seedNow = time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)

func hours(n int) time.Time { return seedNow.Add(time.Duration(n) * time.Hour) }

// best is the node-a best-candidate fixture of the seeded assignment.
func best() *assignment.Candidate {
	return &assignment.Candidate{
		NodeID: "node-a", PickupDepartureID: "dep-a", PickupCutoffAt: hours(5),
		Legs: []string{"leg-1"}, DeliveryWaveID: "wave-1", ETA: hours(30), Cost: 130,
	}
}

// The binary migrates a database itself (idempotently: pgtest's databases are
// already migrated, a second run must be a no-op), then reads through MCP
// tools exactly what the domain's own write path persisted (Queue + Release,
// the aggregate constructors the write use cases and the Kafka consumers
// share), and its reads add no outbox row.
func TestMCPAgainstPostgres_ReadsWhatTheWritePathSavedAndNeverWrites(t *testing.T) {
	url := pgtest.URL(t) // a fresh, already migrated database
	ctx := context.Background()

	pool, err := postgres.NewPool(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	countOutbox := func() int {
		t.Helper()
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM outbox_events`).Scan(&n); err != nil {
			t.Fatalf("count outbox_events: %v", err)
		}
		return n
	}

	seedThroughDomain(t, ctx, pool)
	before := countOutbox()

	deps, closeFn, err := buildDeps(ctx, quietLogger(), url, url)
	if err != nil {
		t.Fatalf("buildDeps: %v", err)
	}
	t.Cleanup(closeFn)

	ct, st := sdk.NewInMemoryTransports()
	ss, err := inboundmcp.NewServer(deps).Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	session, err := sdk.NewClient(&sdk.Implementation{Name: "t", Version: "0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	assertSeededReads(t, session)

	if after := countOutbox(); after != before {
		t.Fatalf("outbox rows %d -> %d: the MCP binary wrote an event", before, after)
	}
}

// seedThroughDomain queues one assignment on node-a and releases it there,
// persisting each step through the Postgres repository: the same Save the
// write use cases and the consumers end on.
func seedThroughDomain(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	repo := postgres.NewAssignmentRepository(pool)
	a, _, err := assignment.Queue(assignment.New{
		ID: assignment.ID("fa-1b4e28ba"), OrderID: "ord-1", GroupNo: 1, DestinationPostalPrefix: "30301",
		Promise: assignment.PromiseConstraint{
			PromiseID: "prm-1", Tier: assignment.TierTwoDay, DeliverByLatest: hours(60), WitnessNodeID: "node-a",
		},
		Lines: []assignment.Line{{LineNo: 1, CatalogItemID: "cat-1", Quantity: 2}},
	}, best(), hours(2), seedNow)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Save(ctx, a); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Release(best(),
		[]assignment.Line{{LineNo: 1, CatalogItemID: "cat-1", ListingID: "lst-1", Quantity: 2}}, hours(2)); err != nil {
		t.Fatal(err)
	}
	if err := repo.Save(ctx, a); err != nil {
		t.Fatal(err)
	}
}

// assertSeededReads reads back, through every MCP tool, what
// seedThroughDomain persisted.
func assertSeededReads(t *testing.T, session *sdk.ClientSession) {
	t.Helper()
	ctx := context.Background()
	call := func(name string, args map[string]any) map[string]any {
		t.Helper()
		res, err := session.CallTool(ctx, &sdk.CallToolParams{Name: name, Arguments: args})
		if err != nil || res.IsError {
			t.Fatalf("%s: err=%v res=%+v", name, err, res)
		}
		return res.StructuredContent.(map[string]any)
	}

	page := call("list_assignments", map[string]any{"orderId": "ord-1"})
	items := page["assignments"].([]any)
	if len(items) != 1 {
		t.Fatalf("list_assignments = %v, want the one seeded assignment", page)
	}
	first := items[0].(map[string]any)
	if first["assignmentId"] != "fa-1b4e28ba" || first["state"] != "RELEASED" {
		t.Fatalf("list_assignments[0] = %v", first)
	}

	one := call("get_assignment", map[string]any{"assignmentId": "fa-1b4e28ba"})
	if one["orderId"] != "ord-1" || one["state"] != "RELEASED" || one["assignmentVersion"] != 2.0 {
		t.Fatalf("get_assignment = %v", one)
	}
	releases := one["releases"].([]any)
	if len(releases) != 1 {
		t.Fatalf("releases = %v, want one", one)
	}
	rel := releases[0].(map[string]any)
	if rel["nodeId"] != "node-a" || rel["outcome"] != "PENDING" {
		t.Fatalf("release = %v", rel)
	}
}
