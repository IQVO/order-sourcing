//go:build integration

package main

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2aclient"
	"github.com/jackc/pgx/v5/pgxpool"
	// The pool below is a pgx pool: this fleet's rule (and cmd/api's own
	// integration test) requires the testcontainers postgres import alongside
	// it in this package; the database itself comes from internal/testing/pgtest.
	_ "github.com/testcontainers/testcontainers-go/modules/postgres"

	inbounda2a "github.com/claudioed/order-sourcing/internal/adapters/inbound/a2a"
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
// already migrated, a second run must be a no-op), then reads through the A2A
// skills, over the real router and the real JSON-RPC wire, exactly what the
// domain's own write path persisted (Queue + Release, the aggregate
// constructors the write use cases and the Kafka consumers share). The agent
// is read-only: its reads add no outbox row.
func TestA2AAgainstPostgres_ReadsWhatTheWritePathSavedAndNeverWrites(t *testing.T) {
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

	srv := httptest.NewServer(newRouter(inbounda2a.Handler(inbounda2a.NewExecutor(deps)), testCard(), quietLogger(), "order-sourcing-a2a"))
	defer srv.Close()
	card := inbounda2a.Card(inbounda2a.Config{AgentURL: srv.URL + "/a2a", Version: "test"})
	client, err := a2aclient.NewFromCard(ctx, card)
	if err != nil {
		t.Fatal(err)
	}

	assertSeededReads(t, client)

	if after := countOutbox(); after != before {
		t.Fatalf("outbox rows %d -> %d: the A2A binary wrote an event", before, after)
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

// assertSeededReads reads back, through every A2A skill, what
// seedThroughDomain persisted.
func assertSeededReads(t *testing.T, client *a2aclient.Client) {
	t.Helper()
	call := func(skill string, args map[string]any) map[string]any {
		t.Helper()
		resp, err := client.SendMessage(context.Background(), &a2a.SendMessageRequest{
			Message: a2a.NewMessage(a2a.MessageRoleUser, a2a.NewDataPart(map[string]any{"skill": skill, "arguments": args})),
		})
		if err != nil {
			t.Fatalf("%s: %v", skill, err)
		}
		msg, ok := resp.(*a2a.Message)
		if !ok || len(msg.Parts) == 0 {
			t.Fatalf("%s: reply = %#v", skill, resp)
		}
		data, ok := msg.Parts[0].Content.(a2a.Data)
		if !ok {
			t.Fatalf("%s: first part = %T, want data", skill, msg.Parts[0].Content)
		}
		raw, _ := json.Marshal(data.Value)
		var env map[string]any
		if err := json.Unmarshal(raw, &env); err != nil {
			t.Fatal(err)
		}
		if env["problem"] != nil {
			t.Fatalf("%s answered a problem: %s", skill, raw)
		}
		return env["data"].(map[string]any)
	}

	page := call("find_assignments", map[string]any{"orderId": "ord-1"})["assignmentList"].(map[string]any)
	items := page["assignments"].([]any)
	if len(items) != 1 {
		t.Fatalf("find_assignments = %v, want the one seeded assignment", page)
	}
	first := items[0].(map[string]any)
	if first["assignmentId"] != "fa-1b4e28ba" || first["state"] != "RELEASED" {
		t.Fatalf("find_assignments[0] = %v", first)
	}

	one := call("assignment_detail", map[string]any{"assignmentId": "fa-1b4e28ba"})["assignment"].(map[string]any)
	if one["orderId"] != "ord-1" || one["state"] != "RELEASED" || one["assignmentVersion"] != 2.0 {
		t.Fatalf("assignment_detail = %v", one)
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
