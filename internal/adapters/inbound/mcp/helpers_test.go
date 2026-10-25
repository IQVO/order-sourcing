package mcp_test

import (
	"context"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	inboundmcp "github.com/claudioed/order-sourcing/internal/adapters/inbound/mcp"
	"github.com/claudioed/order-sourcing/internal/adapters/outbound/memory"
	"github.com/claudioed/order-sourcing/internal/application/usecases"
	"github.com/claudioed/order-sourcing/internal/domain/assignment"
)

// now is the fixture's fixed "now".
var now = time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)

// hours is now plus n hours.
func hours(n int) time.Time { return now.Add(time.Duration(n) * time.Hour) }

// harness is a connected in-memory MCP client over the real server wired to
// the in-memory repository, exactly as cmd/mcp wires it (minus Postgres).
// Aggregates are seeded through the domain's own constructors and the
// repository Save, the write path the REST adapter and the consumers share;
// the MCP surface itself writes nothing.
type harness struct {
	session *sdk.ClientSession
	store   *memory.Store
	repo    *memory.AssignmentRepository
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	store := memory.NewStore()
	repo := store.Assignments()
	deps := inboundmcp.Deps{Queries: usecases.NewQueries(repo)}
	return &harness{session: connectSession(t, deps), store: store, repo: repo}
}

func connectSession(t *testing.T, deps inboundmcp.Deps) *sdk.ClientSession {
	t.Helper()
	ctx := context.Background()
	clientTransport, serverTransport := sdk.NewInMemoryTransports()
	serverSession, err := inboundmcp.NewServer(deps).Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	t.Cleanup(func() { _ = serverSession.Close() })
	client := sdk.NewClient(&sdk.Implementation{Name: "test-client", Version: "0"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// candidate is a best-candidate fixture on one node.
func candidate(node string) *assignment.Candidate {
	return &assignment.Candidate{
		NodeID: node, PickupDepartureID: "dep-" + node, PickupCutoffAt: hours(5),
		Legs: []string{"leg-1", "leg-2"}, DeliveryWaveID: "wave-1", ETA: hours(30), Cost: 130,
	}
}

// newAssignment is the domain input of one assignment fixture.
func newAssignment(id, order string, group int) assignment.New {
	return assignment.New{
		ID: assignment.ID(id), OrderID: order, GroupNo: group, DestinationPostalPrefix: "30301",
		Promise: assignment.PromiseConstraint{
			PromiseID: "prm-" + id, Tier: assignment.TierTwoDay, DeliverByLatest: hours(60), WitnessNodeID: "node-a",
		},
		Lines: []assignment.Line{{LineNo: 1, CatalogItemID: "cat-1", Quantity: 2}},
	}
}

// saveQueued seeds a QUEUED assignment on node-a releasing at hours(2).
func (h *harness) saveQueued(t *testing.T, id, order string, group int) {
	t.Helper()
	a, _, err := assignment.Queue(newAssignment(id, order, group), candidate("node-a"), hours(2), now)
	must(t, err)
	must(t, h.repo.Save(t.Context(), a))
}

// saveReleased seeds a RELEASED assignment (version 2): queued on node-a,
// then released to it.
func (h *harness) saveReleased(t *testing.T, id, order string, group int) {
	t.Helper()
	a, _, err := assignment.Queue(newAssignment(id, order, group), candidate("node-a"), hours(2), now)
	must(t, err)
	must(t, h.repo.Save(t.Context(), a))
	lines := []assignment.Line{{LineNo: 1, CatalogItemID: "cat-1", ListingID: "lst-1", Quantity: 2}}
	_, err = a.Release(candidate("node-a"), lines, hours(2))
	must(t, err)
	must(t, h.repo.Save(t.Context(), a))
}

// call invokes a tool and fails the test on a TRANSPORT/protocol error;
// tool-level failures come back as res.IsError.
func call(t *testing.T, session *sdk.ClientSession, name string, args map[string]any) *sdk.CallToolResult {
	t.Helper()
	res, err := session.CallTool(context.Background(), &sdk.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: transport/protocol error (want a tool result): %v", name, err)
	}
	return res
}

// ok calls a tool, requires success, and returns its structured content.
func (h *harness) ok(t *testing.T, name string, args map[string]any) map[string]any {
	t.Helper()
	res := call(t, h.session, name, args)
	if res.IsError {
		t.Fatalf("%s returned a tool error: %s", name, text(res))
	}
	sc, isMap := res.StructuredContent.(map[string]any)
	if !isMap {
		t.Fatalf("%s: structured content = %#v, want an object", name, res.StructuredContent)
	}
	return sc
}

// failWith calls a tool and requires an isError result whose text starts
// with the slug want.
func failWith(t *testing.T, session *sdk.ClientSession, name string, args map[string]any, want string) {
	t.Helper()
	res := call(t, session, name, args)
	if !res.IsError {
		t.Fatalf("%s: expected an isError tool result, got %#v", name, res.StructuredContent)
	}
	if got := text(res); !strings.HasPrefix(got, want+": ") {
		t.Fatalf("%s: error text %q does not start with %q", name, got, want+": ")
	}
}

func text(res *sdk.CallToolResult) string {
	var b strings.Builder
	for _, c := range res.Content {
		if tc, isText := c.(*sdk.TextContent); isText {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}
