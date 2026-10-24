// Package mcp is the inbound Model Context Protocol adapter: it exposes this
// bounded context to the AI ecosystem as a second driving adapter over the
// same application-layer read use cases the REST adapter uses. It is built on
// the official MCP Go SDK and served over Streamable HTTP only.
//
// Per docs/adr/0006-mcp-server-adoption.md the surface is READ-ONLY: every
// tool reads assignment data (list_assignments, get_assignment); re-optimising
// an assignment stays on REST, and the rest of the lifecycle arrives as Kafka
// events consumed by cmd/api. governance_test.go fails the build on a
// write-verb tool name. This package depends inward on the application layer
// and the domain only, never on an outbound adapter or the REST adapter, and
// nothing else may depend on it (internal/architecture's
// TestMCPAdapterDependencyRule; TestMCPServerIsReadOnly pins that no write use
// case is reachable). The composition root (cmd/mcp) wires the concrete
// repository into the read use cases. There is no auth of any kind
// (fleet-wide revert 2026-09-11; TestNoAuthMiddlewareReintroduced).
package mcp

import (
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// serverName and serverVersion identify this server in the MCP initialize
// handshake.
const (
	serverName    = "order-sourcing-mcp"
	serverVersion = "1.0.0"
)

// instructions is what an MCP client is told about this server at initialize.
const instructions = "Order sourcing of the retail network (read-only): see which vendor node and path will fulfil each " +
	"shipment group of a customer order. list_assignments pages through the fulfillment assignments (optionally only " +
	"those in one state QUEUED, RELEASED, LOCKED, SHIPPED, DELIVERED, UNSOURCEABLE or CANCELLED, of one order, or " +
	"currently on one node); get_assignment reads one in full: its promise constraint, lines, the current candidate " +
	"path with its cost, the VirtualQueue release point, the release history with outcomes, the excluded nodes and the " +
	"lock and shipment facts. Nothing here changes data: asking for an immediate re-optimisation is a REST operation " +
	"of order-sourcing, and every other lifecycle change arrives as a consumed event."

// NewServer builds the MCP server for this bounded context with every tool
// registered.
func NewServer(deps Deps) *mcp.Server {
	server := mcp.NewServer(
		&mcp.Implementation{Name: serverName, Version: serverVersion},
		&mcp.ServerOptions{Instructions: instructions},
	)
	deps.registerTools(server)
	return server
}

// Handler returns the Streamable HTTP handler for the MCP server.
func Handler(server *mcp.Server) http.Handler {
	return mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil)
}
