package mcp

import (
	"context"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/claudioed/order-sourcing/internal/application/repository"
	"github.com/claudioed/order-sourcing/internal/application/usecases"
	"github.com/claudioed/order-sourcing/internal/application/views"
	"github.com/claudioed/order-sourcing/internal/domain/assignment"
	"github.com/claudioed/order-sourcing/internal/domain/shared"
)

// Deps is everything the MCP tools need, injected by the composition root.
// It carries the SAME read use case the REST adapter uses (usecases.Queries:
// GET /assignments and GET /assignments/{assignmentId}); the adapter never
// constructs an outbound adapter itself, and it holds no write use case at
// all (docs/adr/0006). POST /assignments/{id}/reoptimisations stays REST-only.
type Deps struct {
	Queries *usecases.Queries
}

// --- inputs -------------------------------------------------------------------

type assignmentIDInput struct {
	AssignmentID string `json:"assignmentId" jsonschema:"the assignment id, e.g. fa-1b4e28ba-2fa1-4d3c-8f6e-0a1b2c3d4e5f"`
}

type listAssignmentsInput struct {
	State   string `json:"state,omitempty" jsonschema:"optional filter: keep only assignments in this state, one of QUEUED, RELEASED, LOCKED, SHIPPED, DELIVERED, UNSOURCEABLE or CANCELLED; omitted means every state"`
	NodeID  string `json:"nodeId,omitempty" jsonschema:"optional filter: keep only assignments currently on this node (the QUEUED candidate's node, or the node of the latest release); omitted means every node"`
	OrderID string `json:"orderId,omitempty" jsonschema:"optional filter: keep only assignments of this order; omitted means every order"`
	Limit   int    `json:"limit,omitempty" jsonschema:"optional page size, 1..500; omitted or 0 means the default of 100"`
}

// --- outputs (the REST bodies, camelCase like the REST contract) --------------

type promiseView struct {
	PromiseID       string `json:"promiseId"`
	Tier            string `json:"tier"`
	DeliverByLatest string `json:"deliverByLatest"`
	WitnessNodeID   string `json:"witnessNodeId"`
	RevisionNo      int    `json:"revisionNo"`
}

type lineView struct {
	LineNo        int    `json:"lineNo"`
	CatalogItemID string `json:"catalogItemId"`
	ListingID     string `json:"listingId,omitempty"`
	Quantity      int    `json:"quantity"`
}

type candidateView struct {
	NodeID            string   `json:"nodeId"`
	PickupDepartureID string   `json:"pickupDepartureId"`
	PickupCutoffAt    string   `json:"pickupCutoffAt"`
	Legs              []string `json:"legs"`
	DeliveryWaveID    string   `json:"deliveryWaveId"`
	ETA               string   `json:"eta"`
	Cost              int64    `json:"cost"`
}

type releaseView struct {
	ReleaseNo      int    `json:"releaseNo"`
	NodeID         string `json:"nodeId"`
	ReleasedAt     string `json:"releasedAt"`
	RequiredShipBy string `json:"requiredShipBy"`
	Outcome        string `json:"outcome"`
	ReasonCode     string `json:"reasonCode,omitempty"`
}

type excludedNodeView struct {
	NodeID string `json:"nodeId"`
	Reason string `json:"reason"`
}

type assignmentView struct {
	AssignmentID            string             `json:"assignmentId"`
	OrderID                 string             `json:"orderId"`
	GroupNo                 int                `json:"groupNo"`
	DestinationPostalPrefix string             `json:"destinationPostalPrefix"`
	State                   string             `json:"state"`
	AssignmentVersion       int64              `json:"assignmentVersion"`
	Promise                 promiseView        `json:"promise"`
	Lines                   []lineView         `json:"lines"`
	Candidate               *candidateView     `json:"candidate,omitempty"`
	ReleaseAt               string             `json:"releaseAt,omitempty"`
	Releases                []releaseView      `json:"releases"`
	ExcludedNodes           []excludedNodeView `json:"excludedNodes"`
	LockedAt                string             `json:"lockedAt,omitempty"`
	LockReason              string             `json:"lockReason,omitempty"`
	PickupDepartureID       string             `json:"pickupDepartureId,omitempty"`
	TrackingNumbers         []string           `json:"trackingNumbers"`
	ShippedAt               string             `json:"shippedAt,omitempty"`
	DeliveredAt             string             `json:"deliveredAt,omitempty"`
	CancelReason            string             `json:"cancelReason,omitempty"`
}

type assignmentListView struct {
	Assignments []assignmentView `json:"assignments"`
}

// --- mapping ------------------------------------------------------------------

// instant formats an instant as RFC 3339 UTC; the zero time is "" (omitted).
func instant(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func toPromise(p views.Promise) promiseView {
	return promiseView{
		PromiseID: p.PromiseID, Tier: p.Tier, DeliverByLatest: instant(p.DeliverByLatest),
		WitnessNodeID: p.WitnessNodeID, RevisionNo: p.RevisionNo,
	}
}

func toLines(lines []views.Line) []lineView {
	out := make([]lineView, 0, len(lines))
	for _, l := range lines {
		out = append(out, lineView{LineNo: l.LineNo, CatalogItemID: l.CatalogItemID, ListingID: l.ListingID, Quantity: l.Quantity})
	}
	return out
}

func toCandidate(c *views.Candidate) *candidateView {
	if c == nil {
		return nil
	}
	return &candidateView{
		NodeID: c.NodeID, PickupDepartureID: c.PickupDepartureID, PickupCutoffAt: instant(c.PickupCutoffAt),
		Legs: append([]string{}, c.Legs...), DeliveryWaveID: c.DeliveryWaveID, ETA: instant(c.ETA), Cost: c.Cost,
	}
}

func toReleases(releases []views.Release) []releaseView {
	out := make([]releaseView, 0, len(releases))
	for _, r := range releases {
		out = append(out, releaseView{
			ReleaseNo: r.ReleaseNo, NodeID: r.NodeID, ReleasedAt: instant(r.ReleasedAt),
			RequiredShipBy: instant(r.RequiredShipBy), Outcome: r.Outcome, ReasonCode: r.ReasonCode,
		})
	}
	return out
}

func toExcluded(excluded []views.ExcludedNode) []excludedNodeView {
	out := make([]excludedNodeView, 0, len(excluded))
	for _, e := range excluded {
		out = append(out, excludedNodeView{NodeID: e.NodeID, Reason: e.Reason})
	}
	return out
}

func toAssignment(v views.Assignment) assignmentView {
	return assignmentView{
		AssignmentID: v.AssignmentID, OrderID: v.OrderID, GroupNo: v.GroupNo,
		DestinationPostalPrefix: v.DestinationPostalPrefix, State: v.State, AssignmentVersion: v.AssignmentVersion,
		Promise: toPromise(v.Promise), Lines: toLines(v.Lines), Candidate: toCandidate(v.Candidate),
		ReleaseAt: instant(v.ReleaseAt), Releases: toReleases(v.Releases), ExcludedNodes: toExcluded(v.ExcludedNodes),
		LockedAt: instant(v.LockedAt), LockReason: v.LockReason, PickupDepartureID: v.PickupDepartureID,
		TrackingNumbers: append([]string{}, v.TrackingNumbers...), ShippedAt: instant(v.ShippedAt),
		DeliveredAt: instant(v.DeliveredAt), CancelReason: v.CancelReason,
	}
}

// --- handlers -----------------------------------------------------------------

func (d Deps) listAssignments(ctx context.Context, in listAssignmentsInput) (assignmentListView, error) {
	if in.Limit < 0 || in.Limit > repository.MaxLimit {
		return assignmentListView{}, mapError(ctx, shared.Invalid("limit must be between 1 and %d", repository.MaxLimit))
	}
	list, err := d.Queries.ListAssignments(ctx, repository.AssignmentFilter{
		State: assignment.State(in.State), NodeID: in.NodeID, OrderID: in.OrderID, Limit: in.Limit,
	})
	if err != nil {
		return assignmentListView{}, mapError(ctx, err)
	}
	out := make([]assignmentView, 0, len(list))
	for _, a := range list {
		out = append(out, toAssignment(a))
	}
	return assignmentListView{Assignments: out}, nil
}

func (d Deps) getAssignment(ctx context.Context, in assignmentIDInput) (assignmentView, error) {
	v, err := d.Queries.GetAssignment(ctx, assignment.ID(in.AssignmentID))
	if err != nil {
		return assignmentView{}, mapError(ctx, err)
	}
	return toAssignment(v), nil
}

// --- registry -----------------------------------------------------------------

// registerTools adds the two read-only tools. Every tool is annotated
// read-only, idempotent and closed-world; TestToolSurface pins the set and
// fails the build on any write-verb name (docs/adr/0006).
func (d Deps) registerTools(server *mcp.Server) {
	closedWorld := false
	readOnly := &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: &closedWorld}

	addTool(server, &mcp.Tool{
		Name: "list_assignments",
		Description: "List fulfillment assignments, ordered by assignmentId ascending: which vendor node and path will " +
			"fulfil each shipment group of a customer order, in which state (QUEUED, RELEASED, LOCKED, SHIPPED, DELIVERED, " +
			"UNSOURCEABLE, CANCELLED). Filters combine with AND and are all optional: state, orderId, and nodeId (the " +
			"current candidate node while QUEUED, the node of the latest release later). Each item carries the promise " +
			"constraint, the lines, the current candidate with its cost, the releaseAt point and a cap of limit items " +
			"(default 100, maximum 500). Read-only; an unknown state or a limit outside 1..500 fails with " +
			"validation-failed.",
		Annotations: readOnly,
	}, d.listAssignments)

	addTool(server, &mcp.Tool{
		Name: "get_assignment",
		Description: "Read one fulfillment assignment in full by id: the promise constraint it must keep (tier, " +
			"deliverByLatest, witness node), its lines, the current candidate node and path with the cost in cost points, " +
			"the VirtualQueue release point, the whole release history with each outcome (ACCEPTED, REJECTED, EXPIRED, " +
			"PENDING), the nodes that were excluded and why, and the lock, shipment and delivery facts once they apply. " +
			"Read-only; fails with not-found for an unknown id.",
		Annotations: readOnly,
	}, d.getAssignment)
}

// addTool registers one tool. A handler error is returned to the SDK as the
// handler's error, which the SDK turns into an isError tool result (never a
// transport/protocol failure), carrying the error text.
func addTool[In, Out any](
	server *mcp.Server,
	tool *mcp.Tool,
	handle func(context.Context, In) (Out, error),
) {
	mcp.AddTool(server, tool, func(ctx context.Context, _ *mcp.CallToolRequest, in In) (*mcp.CallToolResult, Out, error) {
		out, err := handle(ctx, in)
		if err != nil {
			var zero Out
			return nil, zero, err
		}
		return nil, out, nil
	})
}
