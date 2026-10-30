package a2a

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"

	"github.com/claudioed/order-sourcing/internal/application/ports"
	"github.com/claudioed/order-sourcing/internal/application/repository"
	"github.com/claudioed/order-sourcing/internal/domain/assignment"
	"github.com/claudioed/order-sourcing/internal/domain/shared"
)

// tracerName is the OTel instrumentation scope for A2A skill spans, mirroring
// the MCP adapter's mcp.tool spans.
const tracerName = "github.com/claudioed/order-sourcing/internal/adapters/inbound/a2a"

// Skill ids advertised on the Agent Card (see Card).
const (
	skillFindAssignments  = "find_assignments"
	skillAssignmentDetail = "assignment_detail"
)

// skillRequest is the wire contract of every skill call: the first message
// part carries (as a Data part, or a Text part holding the same JSON) the
// skill id and its arguments. Strict on purpose: an agent client is
// machine-to-machine, so unknown fields are rejected, never ignored. A
// misspelled name like "ordrId" must not silently drop a filter and answer
// with every order. (Go's encoding/json matches field names
// case-insensitively, so "orderid" still reaches orderId; only genuinely
// unknown names are refused.)
type skillRequest struct {
	Skill     string          `json:"skill"`
	Arguments json.RawMessage `json:"arguments"`
}

// findAssignmentsArgs and assignmentDetailArgs are the per-skill argument
// shapes, the same names the MCP tools take.
type findAssignmentsArgs struct {
	State   string `json:"state"`
	NodeID  string `json:"nodeId"`
	OrderID string `json:"orderId"`
	Limit   int    `json:"limit"`
}

type assignmentDetailArgs struct {
	AssignmentID string `json:"assignmentId"`
}

// describe renders the filters that were given, for the one-line summary.
func (a findAssignmentsArgs) describe() string {
	var parts []string
	for _, f := range []struct{ name, value string }{{"state", a.State}, {"orderId", a.OrderID}, {"nodeId", a.NodeID}} {
		if f.value != "" {
			parts = append(parts, f.name+"="+f.value)
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return " for " + strings.Join(parts, ", ")
}

// answer maps one incoming message to a skill and returns the agent's reply
// Message. It is the whole executor body: a message-only agent, so a skill
// run is exactly one request mapped to one answer.
func answer(ctx context.Context, deps Deps, ec *a2asrv.ExecutorContext) a2a.Event {
	req, problem := parseSkillRequest(ec)
	if problem != nil {
		return reply(ec, envelope{Problem: problem}, problem.summary())
	}

	ctx, span := otel.Tracer(tracerName).Start(ctx, "a2a.skill "+req.Skill)
	defer span.End()
	span.SetAttributes(attribute.String("a2a.skill", req.Skill))

	data, problem := run(ctx, deps, req)
	if problem != nil {
		span.SetStatus(codes.Error, problem.Detail)
		return reply(ec, envelope{Skill: req.Skill, Problem: problem}, problem.summary())
	}
	return reply(ec, envelope{Skill: req.Skill, Data: data}, data.summary)
}

// run dispatches one validated request to its skill.
func run(ctx context.Context, deps Deps, req skillRequest) (*skillData, *problem) {
	switch req.Skill {
	case skillFindAssignments:
		return deps.findAssignments(ctx, req.Arguments)
	case skillAssignmentDetail:
		return deps.assignmentDetail(ctx, req.Arguments)
	default:
		return nil, problemUnknownSkill(req.Skill)
	}
}

// reply builds the agent Message for one skill run: a Data part carrying the
// structured envelope and a Text part carrying the one-line summary, bound to
// the caller's context (and task, when the client referenced one).
func reply(ec *a2asrv.ExecutorContext, env envelope, summary string) a2a.Event {
	return a2a.NewMessageForTask(a2a.MessageRoleAgent, ec, a2a.NewDataPart(env), a2a.NewTextPart(summary))
}

// parseSkillRequest extracts the skillRequest from the message's first part.
// A Data part (or a Text part holding the same JSON object) is accepted;
// anything else is a request-shape problem, answered and not raised: a
// malformed delegation is a business answer for the calling agent, not a
// transport failure.
func parseSkillRequest(ec *a2asrv.ExecutorContext) (skillRequest, *problem) {
	msg := ec.Message
	if msg == nil || len(msg.Parts) == 0 {
		return skillRequest{}, problemMalformedRequest("the message carries no parts")
	}
	raw, err := partJSON(msg.Parts[0])
	if err != nil {
		return skillRequest{}, problemMalformedRequest(fmt.Sprintf("the first message part is not a skill request: %v", err))
	}
	var req skillRequest
	if err := decodeStrict(raw, &req); err != nil {
		return skillRequest{}, problemMalformedRequest(fmt.Sprintf("the skill request is not valid: %v", err))
	}
	if req.Skill == "" {
		return skillRequest{}, problemMalformedRequest(`the skill request carries no "skill" field`)
	}
	return req, nil
}

// partJSON renders one part's content as the JSON bytes of a skill request.
func partJSON(part *a2a.Part) ([]byte, error) {
	switch content := part.Content.(type) {
	case a2a.Data:
		return json.Marshal(content.Value)
	case a2a.Text:
		return []byte(string(content)), nil
	default:
		return nil, errors.New("expected a data part (or a text part holding the skill JSON)")
	}
}

// decodeStrict decodes one JSON document into dst, rejecting unknown fields
// and trailing data. Absent arguments decode as the empty object so each
// skill's own required-field checks produce the specific problem.
func decodeStrict(raw []byte, dst any) error {
	if len(bytes.TrimSpace(raw)) == 0 {
		raw = []byte("{}")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	if dec.More() {
		return errors.New("unexpected data after the JSON document")
	}
	return nil
}

// namedArg is one string argument with its wire name, for error messages.
type namedArg struct{ name, value string }

// nulArgument returns the name of the first argument whose value carries
// U+0000 ("" when none does). PostgreSQL text cannot hold it and a statement
// that carries one aborts the surrounding transaction, so it is refused
// before it reaches a repository, as the REST adapter's rejectNUL does.
func nulArgument(args ...namedArg) string {
	for _, a := range args {
		if strings.ContainsRune(a.value, 0) {
			return a.name
		}
	}
	return ""
}

func nulProblem(name string) *problem {
	return problemMalformedRequest(fmt.Sprintf("a NUL character (U+0000) is not allowed in %s", name))
}

// findAssignments is the list skill: the same ListAssignments read as GET
// /assignments and the list_assignments MCP tool. An unknown state or a
// limit outside 1..500 is answered as validation-failed by the use case.
func (d Deps) findAssignments(ctx context.Context, rawArgs json.RawMessage) (*skillData, *problem) {
	var args findAssignmentsArgs
	if err := decodeStrict(rawArgs, &args); err != nil {
		return nil, problemMalformedRequest(fmt.Sprintf("arguments: %v", err))
	}
	if name := nulArgument(
		namedArg{"state", args.State}, namedArg{"nodeId", args.NodeID}, namedArg{"orderId", args.OrderID},
	); name != "" {
		return nil, nulProblem(name)
	}
	list, err := d.Queries.ListAssignments(ctx, repository.AssignmentFilter{
		State: assignment.State(args.State), NodeID: args.NodeID, OrderID: args.OrderID, Limit: args.Limit,
	})
	if err != nil {
		return nil, problemFromUseCase(ctx, err)
	}
	return toAssignmentList(list, args), nil
}

// assignmentDetail is the single-read skill: the same GetAssignment read as
// GET /assignments/{assignmentId} and the get_assignment MCP tool.
func (d Deps) assignmentDetail(ctx context.Context, rawArgs json.RawMessage) (*skillData, *problem) {
	var args assignmentDetailArgs
	if err := decodeStrict(rawArgs, &args); err != nil {
		return nil, problemMalformedRequest(fmt.Sprintf("arguments: %v", err))
	}
	if strings.TrimSpace(args.AssignmentID) == "" {
		return nil, problemInvalidArgument("assignmentId is required")
	}
	// REST answers a NUL in the URL path as 404: no such resource can exist.
	if nulArgument(namedArg{"assignmentId", args.AssignmentID}) != "" {
		return nil, newProblem(404, "resource-not-found", "Resource not found", "no assignment matches the requested assignmentId")
	}
	v, err := d.Queries.GetAssignment(ctx, assignment.ID(args.AssignmentID))
	if err != nil {
		return nil, problemFromUseCase(ctx, err)
	}
	return toAssignmentDetail(v), nil
}

// problemFromUseCase maps a use case error to a problem answer with the SAME
// classification as the REST adapter (internal/adapters/inbound/http
// problemCatalogue), restricted to what a READ can raise: ports.ErrNotFound is
// 404 not-found, shared.ErrValidation (unknown state, limit out of range) is
// 422 validation-failed, anything else is a 500 internal-error whose detail
// never carries infrastructure text (DSNs, SQL). The agent still receives a
// structured, machine-readable answer in every case.
func problemFromUseCase(ctx context.Context, err error) *problem {
	switch {
	case errors.Is(err, ports.ErrNotFound):
		return newProblem(404, "not-found", "Resource not found", err.Error())
	case errors.Is(err, shared.ErrValidation):
		return newProblem(422, "validation-failed", "Validation failed", err.Error())
	default:
		slog.ErrorContext(ctx, "a2a skill failed with an unexpected error", "error", err)
		return newProblem(500, "internal-error", "Internal server error", "the skill failed; see server logs (trace id in the response metadata)")
	}
}
