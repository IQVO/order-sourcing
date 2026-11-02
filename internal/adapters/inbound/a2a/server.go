// Package a2a is the inbound Agent2Agent adapter: it exposes this bounded
// context to the agent ecosystem as a third driving adapter over the same
// application-layer read use cases the REST and MCP adapters use. It is built
// on the official A2A Go SDK (a2aproject/a2a-go) and speaks JSON-RPC 2.0 over
// HTTP, with the Agent Card served at the well-known path.
//
// Per docs/adr/0009-a2a-inbound-adapter.md, A2A is the DELEGATION channel: an
// agent client sends a goal-shaped skill request as a structured message part
// and this agent answers from its own state through its own use cases. It is
// not a data-sync channel; facts between contexts still travel exclusively as
// CloudEvents on Kafka.
//
// The agent is READ-ONLY, like the MCP surface it derives from
// (docs/adr/0006-mcp-server-adoption.md): both skills read assignment data and
// no write use case is reachable from this package (Deps carries Queries
// only; internal/architecture's TestA2AAgentIsReadOnly pins it).
// POST /assignments/{id}/reoptimisations stays a REST operation and every other
// lifecycle change arrives as a consumed event.
//
// This package depends inward on the application layer and the domain only,
// never on an outbound adapter, and nothing else may depend on it
// (TestA2AAdapterDependencyRule). It never constructs an A2A client: this
// context calls no sibling. There is no authentication of any kind
// (fleet-wide revert 2026-09-11; TestNoAuthMiddlewareReintroduced). Domain
// structs never leak across the message boundary.
package a2a

import (
	"context"
	"iter"
	"net/http"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"

	"github.com/claudioed/order-sourcing/internal/application/usecases"
)

// Config carries the deployment-specific identity of this agent: the absolute
// URL clients use to reach its JSON-RPC endpoint (advertised on the Agent
// Card) and the card version. It is supplied by the composition root from
// A2A_AGENT_URL; the card MUST advertise a URL the agent clients can actually
// dial (in-cluster: http://<release>-a2a.<ns>.svc.cluster.local:8093/).
type Config struct {
	AgentURL string
	Version  string
}

// Deps is everything the A2A skills need, injected by the composition root.
// It carries the SAME read use case the REST and MCP adapters use
// (usecases.Queries: GET /assignments and GET /assignments/{assignmentId});
// the adapter never constructs an outbound adapter itself and holds no write
// use case at all (docs/adr/0009).
type Deps struct {
	Queries *usecases.Queries
}

// Card builds this agent's Agent Card. The surface is two curated,
// goal-shaped READ skills (not one skill per endpoint), derived from the two
// MCP tools: find_assignments answers "which shipment groups match", and
// assignment_detail answers "where does this one go and why". The card
// declares no security schemes on purpose: this surface is unauthenticated by
// the same fleet-wide decision as REST and MCP.
func Card(cfg Config) *a2a.AgentCard {
	return &a2a.AgentCard{
		Name:        "order-sourcing",
		Description: "Order Sourcing agent of the retail network (read-only): answers which vendor node and path will fulfil each shipment group of a customer order, in which state, and why (the promise it must keep, the candidate with its cost, the VirtualQueue release point, the release history, the excluded nodes and the lock and shipment facts). It never changes data: asking for an immediate re-optimisation is a REST operation of order-sourcing, and every other lifecycle change arrives as a consumed event.",
		Version:     cfg.Version,
		Provider: &a2a.AgentProvider{
			Org: "IQVO",
			URL: "https://github.com/IQVO/order-sourcing",
		},
		SupportedInterfaces: []*a2a.AgentInterface{
			a2a.NewAgentInterface(cfg.AgentURL, a2a.TransportProtocolJSONRPC),
		},
		DefaultInputModes:  []string{"application/json", "text/plain"},
		DefaultOutputModes: []string{"application/json", "text/plain"},
		Capabilities:       a2a.AgentCapabilities{Streaming: false},
		Skills: []a2a.AgentSkill{
			{
				ID:          skillFindAssignments,
				Name:        "Find fulfillment assignments",
				Description: "Lists the fulfillment assignments (one per shipment group of a customer order), ordered by assignmentId ascending, optionally narrowed by state (QUEUED, RELEASED, LOCKED, SHIPPED, DELIVERED, UNSOURCEABLE or CANCELLED), orderId and nodeId (the candidate node while QUEUED, the node of the latest release later); filters combine with AND. Each item carries the promise constraint, the lines, the current candidate with its cost and the releaseAt point. limit caps the page, 1..500 (default 100). Read-only.",
				Tags:        []string{"order-sourcing", "assignment", "read"},
				InputModes:  []string{"application/json"},
				Examples: []string{
					`{"skill":"find_assignments","arguments":{"orderId":"ord-1"}}`,
					`{"skill":"find_assignments","arguments":{"state":"UNSOURCEABLE","limit":50}}`,
				},
			},
			{
				ID:          skillAssignmentDetail,
				Name:        "Explain one fulfillment assignment",
				Description: "Reads one fulfillment assignment in full by assignmentId: the promise it must keep (tier, deliverByLatest, witness node), its lines, the current candidate node and path with the cost in cost points, the VirtualQueue release point, the whole release history with each outcome (ACCEPTED, REJECTED, EXPIRED, PENDING), the nodes that were excluded and why, and the lock, shipment and delivery facts once they apply. Answered as a not-found problem for an unknown id. Read-only.",
				Tags:        []string{"order-sourcing", "assignment", "read"},
				InputModes:  []string{"application/json"},
				Examples: []string{
					`{"skill":"assignment_detail","arguments":{"assignmentId":"fa-1b4e28ba-2fa1-4d3c-8f6e-0a1b2c3d4e5f"}}`,
				},
			},
		},
	}
}

// NewExecutor returns the AgentExecutor that maps an incoming message to a
// skill and answers with a single agent Message (a message-only agent in A2A
// terms): both skills are request/response reads, so there is no long-running
// Task lifecycle to track. The response carries the caller's contextId (and
// taskId when the client referenced one) so a delegating saga can group its
// interactions.
func NewExecutor(deps Deps) a2asrv.AgentExecutor {
	return a2asrv.AgentExecutorFunc(func(ctx context.Context, ec *a2asrv.ExecutorContext) iter.Seq2[a2a.Event, error] {
		return func(yield func(a2a.Event, error) bool) {
			yield(answer(ctx, deps, ec), nil)
		}
	})
}

// Handler wires the executor into the transport-agnostic A2A request handler
// and wraps it in the JSON-RPC 2.0 HTTP handler, the only transport this
// agent ships (docs/adr/0009), mirroring the MCP adapter's "Streamable HTTP
// only" minimalism.
func Handler(executor a2asrv.AgentExecutor) http.Handler {
	return a2asrv.NewJSONRPCHandler(a2asrv.NewHandler(executor))
}
