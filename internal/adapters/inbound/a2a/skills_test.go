package a2a

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2aclient"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/claudioed/order-sourcing/internal/adapters/outbound/memory"
	"github.com/claudioed/order-sourcing/internal/application/ports"
	"github.com/claudioed/order-sourcing/internal/application/repository"
	"github.com/claudioed/order-sourcing/internal/application/usecases"
	"github.com/claudioed/order-sourcing/internal/domain/assignment"
)

var now = time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)

func hours(n int) time.Time { return now.Add(time.Duration(n) * time.Hour) }

// ---- fixtures: seeded through the domain's own constructors and the repository
// Save, the write path REST and the consumers share; the A2A surface writes
// nothing -------------------------------------------------------------------

type harness struct {
	deps Deps
	repo *memory.AssignmentRepository
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	repo := memory.NewStore().Assignments()
	return &harness{deps: Deps{Queries: usecases.NewQueries(repo)}, repo: repo}
}

func candidate(node string) *assignment.Candidate {
	return &assignment.Candidate{
		NodeID: node, PickupDepartureID: "dep-" + node, PickupCutoffAt: hours(5),
		Legs: []string{"leg-1", "leg-2"}, DeliveryWaveID: "wave-1", ETA: hours(30), Cost: 130,
	}
}

func newAssignment(id, order string, group int) assignment.New {
	return assignment.New{
		ID: assignment.ID(id), OrderID: order, GroupNo: group, DestinationPostalPrefix: "30301",
		Promise: assignment.PromiseConstraint{
			PromiseID: "prm-" + id, Tier: assignment.TierTwoDay, DeliverByLatest: hours(60), WitnessNodeID: "node-a",
		},
		Lines: []assignment.Line{{LineNo: 1, CatalogItemID: "cat-1", Quantity: 2}},
	}
}

func (h *harness) saveQueued(t *testing.T, id, order string, group int) {
	t.Helper()
	a, _, err := assignment.Queue(newAssignment(id, order, group), candidate("node-a"), hours(2), now)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.repo.Save(t.Context(), a); err != nil {
		t.Fatal(err)
	}
}

func (h *harness) saveReleased(t *testing.T, id, order string, group int) {
	t.Helper()
	a, _, err := assignment.Queue(newAssignment(id, order, group), candidate("node-a"), hours(2), now)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.repo.Save(t.Context(), a); err != nil {
		t.Fatal(err)
	}
	lines := []assignment.Line{{LineNo: 1, CatalogItemID: "cat-1", ListingID: "lst-1", Quantity: 2}}
	if _, err := a.Release(candidate("node-a"), lines, hours(2)); err != nil {
		t.Fatal(err)
	}
	if err := h.repo.Save(t.Context(), a); err != nil {
		t.Fatal(err)
	}
}

// ---- the wire: JSON-RPC over HTTP, driven by the SDK's own client ------------

// wireEnvelope is a TEST-LOCAL copy of the answer contract. If the adapter's
// envelope drifts, this suite fails instead of silently following it.
type wireEnvelope struct {
	Skill string `json:"skill"`
	Data  *struct {
		AssignmentList *struct {
			Assignments []wireAssignment `json:"assignments"`
		} `json:"assignmentList"`
		Assignment *wireAssignment `json:"assignment"`
	} `json:"data"`
	Problem *struct {
		Type   string `json:"type"`
		Title  string `json:"title"`
		Detail string `json:"detail"`
		Status int    `json:"status"`
	} `json:"problem"`
}

type wireAssignment struct {
	AssignmentID      string `json:"assignmentId"`
	OrderID           string `json:"orderId"`
	GroupNo           int    `json:"groupNo"`
	State             string `json:"state"`
	AssignmentVersion int64  `json:"assignmentVersion"`
	Promise           struct {
		PromiseID     string `json:"promiseId"`
		Tier          string `json:"tier"`
		WitnessNodeID string `json:"witnessNodeId"`
	} `json:"promise"`
	Lines []struct {
		CatalogItemID string `json:"catalogItemId"`
		ListingID     string `json:"listingId"`
		Quantity      int    `json:"quantity"`
	} `json:"lines"`
	Candidate *struct {
		NodeID string   `json:"nodeId"`
		Legs   []string `json:"legs"`
		Cost   int64    `json:"cost"`
	} `json:"candidate"`
	Releases []struct {
		ReleaseNo int    `json:"releaseNo"`
		NodeID    string `json:"nodeId"`
		Outcome   string `json:"outcome"`
	} `json:"releases"`
	ExcludedNodes   []json.RawMessage `json:"excludedNodes"`
	TrackingNumbers []string          `json:"trackingNumbers"`
}

type wire struct{ client *a2aclient.Client }

func newWire(t *testing.T, deps Deps) *wire {
	t.Helper()
	srv := httptest.NewServer(Handler(NewExecutor(deps)))
	t.Cleanup(srv.Close)
	card := Card(Config{AgentURL: srv.URL + "/", Version: "test"})
	client, err := a2aclient.NewFromCard(context.Background(), card)
	if err != nil {
		t.Fatalf("NewFromCard: %v", err)
	}
	return &wire{client: client}
}

// call sends one message part and returns the decoded envelope, the one-line
// text summary and the raw data-part JSON of the agent's reply.
func (w *wire) call(t *testing.T, part *a2a.Part) (wireEnvelope, string, string) {
	t.Helper()
	resp, err := w.client.SendMessage(context.Background(), &a2a.SendMessageRequest{Message: a2a.NewMessage(a2a.MessageRoleUser, part)})
	if err != nil {
		t.Fatalf("SendMessage (the answer must be in-band, never a JSON-RPC error): %v", err)
	}
	msg, ok := resp.(*a2a.Message)
	if !ok || len(msg.Parts) < 2 {
		t.Fatalf("reply = %T, want a message with a data and a text part", resp)
	}
	data, ok := msg.Parts[0].Content.(a2a.Data)
	if !ok {
		t.Fatalf("first reply part is %T, want data", msg.Parts[0].Content)
	}
	raw, _ := json.Marshal(data.Value)
	var env wireEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("reply is not the skill envelope: %v (%s)", err, raw)
	}
	text, _ := msg.Parts[1].Content.(a2a.Text)
	return env, string(text), string(raw)
}

func (w *wire) skill(t *testing.T, skill string, args map[string]any) (wireEnvelope, string) {
	t.Helper()
	env, text, _ := w.call(t, a2a.NewDataPart(map[string]any{"skill": skill, "arguments": args}))
	return env, text
}

func wantProblem(t *testing.T, env wireEnvelope, status int, slug string) {
	t.Helper()
	if env.Problem == nil {
		t.Fatalf("expected a %d problem, got data: %+v", status, env.Data)
	}
	if env.Problem.Status != status || env.Problem.Type != problemBaseURI+slug {
		t.Fatalf("problem = %+v, want %d %s", env.Problem, status, slug)
	}
	if env.Data != nil {
		t.Fatalf("a problem answer must carry no data: %+v", env.Data)
	}
}

func TestWireFindAssignmentsListsEverything(t *testing.T) {
	h := newHarness(t)
	h.saveQueued(t, "fa-a", "ord-1", 1)
	h.saveReleased(t, "fa-b", "ord-2", 1)
	w := newWire(t, h.deps)

	env, text := w.skill(t, skillFindAssignments, map[string]any{})
	if env.Skill != skillFindAssignments || env.Problem != nil || env.Data.AssignmentList == nil {
		t.Fatalf("envelope = %+v", env)
	}
	items := env.Data.AssignmentList.Assignments
	if len(items) != 2 || items[0].AssignmentID != "fa-a" || items[1].AssignmentID != "fa-b" {
		t.Fatalf("assignments = %+v, want fa-a, fa-b ordered by id", items)
	}
	assertQueuedFirst(t, items[0])
	if text != "2 assignment(s)" {
		t.Errorf("summary = %q", text)
	}
}

// assertQueuedFirst checks the QUEUED fixture fa-a as it comes over the wire.
func assertQueuedFirst(t *testing.T, first wireAssignment) {
	t.Helper()
	if first.OrderID != "ord-1" || first.State != "QUEUED" || first.AssignmentVersion != 1 {
		t.Fatalf("first = %+v", first)
	}
	if first.Promise.Tier != "TWO_DAY" || first.Promise.WitnessNodeID != "node-a" || len(first.Lines) != 1 {
		t.Fatalf("first promise/lines = %+v", first)
	}
	if first.Candidate == nil || first.Candidate.NodeID != "node-a" || first.Candidate.Cost != 130 {
		t.Fatalf("first candidate = %+v", first.Candidate)
	}
}

func TestWireFindAssignmentsFilters(t *testing.T) {
	h := newHarness(t)
	h.saveQueued(t, "fa-a", "ord-1", 1)
	h.saveReleased(t, "fa-b", "ord-2", 1)
	w := newWire(t, h.deps)

	for name, tc := range map[string]struct {
		args map[string]any
		want []string
		text string
	}{
		"state":   {map[string]any{"state": "QUEUED"}, []string{"fa-a"}, "1 assignment(s) for state=QUEUED"},
		"order":   {map[string]any{"orderId": "ord-2"}, []string{"fa-b"}, "1 assignment(s) for orderId=ord-2"},
		"node":    {map[string]any{"nodeId": "node-a"}, []string{"fa-a", "fa-b"}, "2 assignment(s) for nodeId=node-a"},
		"limit":   {map[string]any{"limit": 1}, []string{"fa-a"}, "1 assignment(s)"},
		"and":     {map[string]any{"state": "RELEASED", "orderId": "ord-1"}, nil, "0 assignment(s) for state=RELEASED, orderId=ord-1"},
		"limit0":  {map[string]any{"limit": 0}, []string{"fa-a", "fa-b"}, "2 assignment(s)"},
		"no-node": {map[string]any{"nodeId": "node-z"}, nil, "0 assignment(s) for nodeId=node-z"},
	} {
		t.Run(name, func(t *testing.T) {
			env, text := w.skill(t, skillFindAssignments, tc.args)
			if env.Problem != nil || env.Data == nil || env.Data.AssignmentList == nil {
				t.Fatalf("envelope = %+v", env)
			}
			var got []string
			for _, a := range env.Data.AssignmentList.Assignments {
				got = append(got, a.AssignmentID)
			}
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Fatalf("ids = %v, want %v", got, tc.want)
			}
			if text != tc.text {
				t.Errorf("summary = %q, want %q", text, tc.text)
			}
		})
	}
}

// An empty page is an empty list on the wire, never null.
func TestWireFindAssignmentsEmptyPageIsAnEmptyArray(t *testing.T) {
	w := newWire(t, newHarness(t).deps)
	_, _, raw := w.call(t, a2a.NewDataPart(map[string]any{"skill": skillFindAssignments}))
	if !strings.Contains(raw, `"assignments":[]`) {
		t.Fatalf("an empty page must serialise as an empty array: %s", raw)
	}
}

// assertReleasedDetail checks the history and the empty-list shape of the
// RELEASED fixture fa-b.
func assertReleasedDetail(t *testing.T, a *wireAssignment) {
	t.Helper()
	if len(a.Releases) != 1 || a.Releases[0].NodeID != "node-a" || a.Releases[0].Outcome != "PENDING" {
		t.Fatalf("releases = %+v, want the one pending release on node-a", a.Releases)
	}
	if a.Lines[0].ListingID != "lst-1" {
		t.Fatalf("lines = %+v, want the listing chosen at release", a.Lines)
	}
	if a.ExcludedNodes == nil || a.TrackingNumbers == nil {
		t.Fatalf("excluded/tracking = %v / %v (empty lists must not be null)", a.ExcludedNodes, a.TrackingNumbers)
	}
}

func TestWireAssignmentDetailReadsOneInFull(t *testing.T) {
	h := newHarness(t)
	h.saveReleased(t, "fa-b", "ord-2", 3)
	w := newWire(t, h.deps)

	env, text := w.skill(t, skillAssignmentDetail, map[string]any{"assignmentId": "fa-b"})
	if env.Skill != skillAssignmentDetail || env.Problem != nil || env.Data == nil || env.Data.Assignment == nil {
		t.Fatalf("envelope = %+v", env)
	}
	a := env.Data.Assignment
	if a.AssignmentID != "fa-b" || a.OrderID != "ord-2" || a.GroupNo != 3 || a.State != "RELEASED" || a.AssignmentVersion != 2 {
		t.Fatalf("assignment = %+v", a)
	}
	assertReleasedDetail(t, a)
	if !strings.HasPrefix(text, "fa-b: RELEASED, order ord-2 group 3,") || !strings.Contains(text, "(version 2)") {
		t.Errorf("summary = %q", text)
	}
}

// Reply text part and data part agree, and a Text part holding the skill JSON
// is accepted like a Data part.
func TestWireAcceptsATextPartHoldingTheSkillJSON(t *testing.T) {
	h := newHarness(t)
	h.saveQueued(t, "fa-a", "ord-1", 1)
	w := newWire(t, h.deps)

	env, _, _ := w.call(t, a2a.NewTextPart(`{"skill":"assignment_detail","arguments":{"assignmentId":"fa-a"}}`))
	if env.Problem != nil || env.Data == nil || env.Data.Assignment == nil || env.Data.Assignment.AssignmentID != "fa-a" {
		t.Fatalf("envelope = %+v", env)
	}
}

// In-band problems carry the SAME status classification as the REST adapter:
// 404 not-found, 422 validation-failed (unknown state, limit out of range),
// 400 for request-shape failures; never a JSON-RPC error.
func TestWireProblemsAreAnsweredInBand(t *testing.T) {
	h := newHarness(t)
	h.saveQueued(t, "fa-a", "ord-1", 1)
	w := newWire(t, h.deps)

	cases := []struct {
		name   string
		part   *a2a.Part
		status int
		slug   string
		detail string
	}{
		{"unknown skill", a2a.NewDataPart(map[string]any{"skill": "reoptimise_assignment", "arguments": map[string]any{}}), 400, "unknown-skill", "reoptimise_assignment"},
		{"no skill field", a2a.NewDataPart(map[string]any{"arguments": map[string]any{}}), 400, "malformed-request", `no "skill" field`},
		{"unknown top-level field", a2a.NewDataPart(map[string]any{"skill": skillAssignmentDetail, "args": map[string]any{}}), 400, "malformed-request", "unknown field"},
		{"text part not json", a2a.NewTextPart("where does my order ship from?"), 400, "malformed-request", "not valid"},
		{"trailing data", a2a.NewTextPart(`{"skill":"find_assignments"} {"skill":"find_assignments"}`), 400, "malformed-request", "unexpected data after"},
		{"unknown argument", a2a.NewDataPart(map[string]any{"skill": skillFindAssignments, "arguments": map[string]any{"ordrId": "ord-1"}}), 400, "malformed-request", "unknown field"},
		{"wrong type", a2a.NewDataPart(map[string]any{"skill": skillFindAssignments, "arguments": map[string]any{"limit": "5"}}), 400, "malformed-request", "limit"},
		{"fractional limit", a2a.NewDataPart(map[string]any{"skill": skillFindAssignments, "arguments": map[string]any{"limit": 1.5}}), 400, "malformed-request", "limit"},
		{"trailing data in arguments", a2a.NewTextPart(`{"skill":"find_assignments","arguments":{} }  []`), 400, "malformed-request", "unexpected data after"},
		{"unknown state", a2a.NewDataPart(map[string]any{"skill": skillFindAssignments, "arguments": map[string]any{"state": "SHIPPING"}}), 422, "validation-failed", "SHIPPING"},
		{"lower-case state", a2a.NewDataPart(map[string]any{"skill": skillFindAssignments, "arguments": map[string]any{"state": "queued"}}), 422, "validation-failed", "queued"},
		{"limit too large", a2a.NewDataPart(map[string]any{"skill": skillFindAssignments, "arguments": map[string]any{"limit": 501}}), 422, "validation-failed", "between 1 and"},
		{"negative limit", a2a.NewDataPart(map[string]any{"skill": skillFindAssignments, "arguments": map[string]any{"limit": -1}}), 422, "validation-failed", "between 1 and"},
		{"NUL in orderId", a2a.NewDataPart(map[string]any{"skill": skillFindAssignments, "arguments": map[string]any{"orderId": "ord\u0000-1"}}), 400, "malformed-request", "NUL"},
		{"NUL in nodeId", a2a.NewDataPart(map[string]any{"skill": skillFindAssignments, "arguments": map[string]any{"nodeId": "\u0000"}}), 400, "malformed-request", "NUL"},
		{"NUL in state", a2a.NewDataPart(map[string]any{"skill": skillFindAssignments, "arguments": map[string]any{"state": "QUEUED\u0000"}}), 400, "malformed-request", "NUL"},
		{"missing id", a2a.NewDataPart(map[string]any{"skill": skillAssignmentDetail, "arguments": map[string]any{}}), 400, "invalid-argument", "assignmentId is required"},
		{"absent arguments", a2a.NewDataPart(map[string]any{"skill": skillAssignmentDetail}), 400, "invalid-argument", "assignmentId is required"},
		{"blank id", a2a.NewDataPart(map[string]any{"skill": skillAssignmentDetail, "arguments": map[string]any{"assignmentId": "  "}}), 400, "invalid-argument", "assignmentId is required"},
		{"unknown id", a2a.NewDataPart(map[string]any{"skill": skillAssignmentDetail, "arguments": map[string]any{"assignmentId": "fa-missing"}}), 404, "not-found", "not found"},
		{"NUL in id is a 404 like the REST path", a2a.NewDataPart(map[string]any{"skill": skillAssignmentDetail, "arguments": map[string]any{"assignmentId": "fa-a\u0000"}}), 404, "resource-not-found", "no assignment"},
		{"id is not a string", a2a.NewDataPart(map[string]any{"skill": skillAssignmentDetail, "arguments": map[string]any{"assignmentId": 7}}), 400, "malformed-request", "assignmentId"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env, text, _ := w.call(t, tc.part)
			wantProblem(t, env, tc.status, tc.slug)
			if !strings.Contains(env.Problem.Detail, tc.detail) {
				t.Errorf("detail = %q, want it to mention %q", env.Problem.Detail, tc.detail)
			}
			if !strings.Contains(text, env.Problem.Title) || !strings.Contains(text, env.Problem.Detail) {
				t.Errorf("text summary %q does not carry the problem's title and detail", text)
			}
		})
	}
}

// Every skill, in every outcome, reaches the use cases through the SAME
// request path: a part that is neither data nor text is answered in-band too.
func TestWireFilePartIsAMalformedRequest(t *testing.T) {
	w := newWire(t, newHarness(t).deps)
	env, _, _ := w.call(t, a2a.NewRawPart([]byte("bytes")))
	wantProblem(t, env, 400, "malformed-request")
}

// failingRepo makes every read fail with an infrastructure error.
type failingRepo struct{ ports.AssignmentRepository }

var errInfra = errors.New("dial tcp 10.0.0.9:5432: connect: connection refused (postgres://app:s3cret@db/orders)")

func (failingRepo) Get(context.Context, assignment.ID) (*assignment.Assignment, error) {
	return nil, errInfra
}

func (failingRepo) List(context.Context, repository.AssignmentFilter) ([]*assignment.Assignment, error) {
	return nil, errInfra
}

// An unexpected failure is a 500 internal-error problem whose answer never
// carries infrastructure text.
func TestWireInfrastructureFailureIsAnInternalProblemWithoutLeaks(t *testing.T) {
	w := newWire(t, Deps{Queries: usecases.NewQueries(failingRepo{})})
	for _, tc := range []struct {
		skill string
		args  map[string]any
	}{
		{skillFindAssignments, map[string]any{}},
		{skillAssignmentDetail, map[string]any{"assignmentId": "fa-a"}},
	} {
		t.Run(tc.skill, func(t *testing.T) {
			env, text, raw := w.call(t, a2a.NewDataPart(map[string]any{"skill": tc.skill, "arguments": tc.args}))
			wantProblem(t, env, http.StatusInternalServerError, "internal-error")
			for _, leak := range []string{"s3cret", "5432", "postgres://", "refused"} {
				if strings.Contains(raw, leak) || strings.Contains(text, leak) {
					t.Errorf("the answer leaks %q: %s / %s", leak, raw, text)
				}
			}
		})
	}
}

// The reply is bound to the caller's context, like any A2A answer.
func TestWireReplyCarriesTheCallersContextID(t *testing.T) {
	w := newWire(t, newHarness(t).deps)
	msg := a2a.NewMessage(a2a.MessageRoleUser, a2a.NewDataPart(map[string]any{"skill": skillFindAssignments}))
	msg.ContextID = "ctx-saga-1"
	resp, err := w.client.SendMessage(context.Background(), &a2a.SendMessageRequest{Message: msg})
	if err != nil {
		t.Fatal(err)
	}
	reply, ok := resp.(*a2a.Message)
	if !ok || reply.ContextID != "ctx-saga-1" {
		t.Fatalf("reply = %+v, want the caller's contextId echoed", resp)
	}
}

// One OTel span per skill run, carrying the a2a.skill attribute.
func TestSkillRunEmitsAnA2ASkillSpan(t *testing.T) {
	prev := otel.GetTracerProvider()
	t.Cleanup(func() { otel.SetTracerProvider(prev) })
	recorder := tracetest.NewSpanRecorder()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder)))

	w := newWire(t, newHarness(t).deps)
	w.skill(t, skillFindAssignments, map[string]any{})
	w.skill(t, skillAssignmentDetail, map[string]any{"assignmentId": "fa-missing"})

	got := map[string]string{}
	for _, s := range recorder.Ended() {
		for _, kv := range s.Attributes() {
			if string(kv.Key) == "a2a.skill" {
				got[s.Name()] = kv.Value.AsString()
			}
		}
	}
	want := map[string]string{
		"a2a.skill find_assignments":  "find_assignments",
		"a2a.skill assignment_detail": "assignment_detail",
	}
	for name, skill := range want {
		if got[name] != skill {
			t.Errorf("span %q a2a.skill = %q, want %q (spans: %v)", name, got[name], skill, got)
		}
	}
}

// decodeStrict is the strictness every skill leans on.
func TestDecodeStrict(t *testing.T) {
	var args findAssignmentsArgs
	for name, tc := range map[string]struct {
		in      string
		wantErr bool
	}{
		"empty is the empty object": {"", false},
		"blank is the empty object": {"  \n", false},
		"known field":               {`{"orderId":"o"}`, false},
		"unknown field":             {`{"orderIdd":"o"}`, true},
		"trailing document":         {`{} {}`, true},
		"trailing garbage":          {`{} x`, true},
		"not an object":             {`[1]`, true},
	} {
		t.Run(name, func(t *testing.T) {
			if err := decodeStrict([]byte(tc.in), &args); (err != nil) != tc.wantErr {
				t.Fatalf("decodeStrict(%q) err = %v, wantErr %v", tc.in, err, tc.wantErr)
			}
		})
	}
}
