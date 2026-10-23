package http

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/claudioed/order-sourcing/internal/application/ports"
	"github.com/claudioed/order-sourcing/internal/application/repository"
	"github.com/claudioed/order-sourcing/internal/application/views"
	"github.com/claudioed/order-sourcing/internal/domain/assignment"
	"github.com/claudioed/order-sourcing/internal/domain/shared"
)

var t0 = time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)

func sample() views.Assignment {
	return views.Assignment{
		AssignmentID: "asg-1", OrderID: "ord-1", GroupNo: 1, DestinationPostalPrefix: "30301", State: "QUEUED", AssignmentVersion: 2,
		Promise:   views.Promise{PromiseID: "prm-1", Tier: "TWO_DAY", DeliverByLatest: t0.Add(48 * time.Hour), WitnessNodeID: "n1"},
		Lines:     []views.Line{{LineNo: 1, CatalogItemID: "cat-1", Quantity: 2}},
		Candidate: &views.Candidate{NodeID: "n1", PickupDepartureID: "dep-1", Legs: []string{"dep-2"}, DeliveryWaveID: "wave-1", ETA: t0.Add(30 * time.Hour), Cost: 200},
		ReleaseAt: t0.Add(time.Hour), Releases: []views.Release{{ReleaseNo: 1, NodeID: "n1", Outcome: "REJECTED", ReasonCode: "OOS"}},
		ExcludedNodes: []views.ExcludedNode{{NodeID: "n2", Reason: "REJECTED"}},
	}
}

type fakeUC struct {
	list   []views.Assignment
	err    error
	filter repository.AssignmentFilter
	gotID  assignment.ID
	gotVer int64
}

func (f *fakeUC) ListAssignments(_ context.Context, fl repository.AssignmentFilter) ([]views.Assignment, error) {
	f.filter = fl
	return f.list, f.err
}

func (f *fakeUC) GetAssignment(_ context.Context, id assignment.ID) (views.Assignment, error) {
	f.gotID = id
	return sample(), f.err
}

func (f *fakeUC) Execute(_ context.Context, id assignment.ID, v int64) (views.Assignment, error) {
	f.gotID, f.gotVer = id, v
	return sample(), f.err
}

func call(f *fakeUC, method, target, body string) *httptest.ResponseRecorder {
	h := (&Server{List: f, Get: f, Reoptimise: f}).Handler()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, target, strings.NewReader(body)))
	return rec
}

func TestListMapsFiltersAndShape(t *testing.T) {
	f := &fakeUC{list: []views.Assignment{sample()}}
	rec := call(f, http.MethodGet, "/assignments?state=QUEUED&nodeId=n1&orderId=ord-1&limit=7", "")
	if rec.Code != 200 || f.filter.State != "QUEUED" || f.filter.NodeID != "n1" || f.filter.OrderID != "ord-1" || f.filter.Limit != 7 {
		t.Fatalf("code=%d filter=%+v", rec.Code, f.filter)
	}
	var out map[string][]map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	a := out["assignments"][0]
	for _, k := range []string{"assignmentId", "promise", "lines", "candidate", "releaseAt", "releases", "excludedNodes", "trackingNumbers"} {
		if _, ok := a[k]; !ok {
			t.Errorf("missing %s in %v", k, a)
		}
	}
	for _, k := range []string{"lockedAt", "shippedAt", "deliveredAt", "lockReason", "cancelReason", "pickupDepartureId"} {
		if _, ok := a[k]; ok {
			t.Errorf("unset %s must be absent", k)
		}
	}
	if rec.Header().Get("Content-Type") != "application/json" {
		t.Fatal("content type")
	}
	empty := call(&fakeUC{}, http.MethodGet, "/assignments", "")
	if empty.Code != 200 || strings.TrimSpace(empty.Body.String()) != `{"assignments":[]}` {
		t.Fatalf("empty list = %d %s", empty.Code, empty.Body)
	}
}

func TestListRejectsBadLimits(t *testing.T) {
	for _, q := range []string{"limit=abc", "limit=0", "limit=-3"} {
		rec := call(&fakeUC{}, http.MethodGet, "/assignments?"+q, "")
		if rec.Code != 422 || !strings.Contains(rec.Body.String(), "validation-failed") {
			t.Errorf("%s -> %d %s", q, rec.Code, rec.Body)
		}
	}
}

func TestGetAndReoptimiseSuccess(t *testing.T) {
	f := &fakeUC{}
	if rec := call(f, http.MethodGet, "/assignments/asg-1", ""); rec.Code != 200 || f.gotID != "asg-1" {
		t.Fatalf("get = %d", rec.Code)
	}
	rec := call(f, http.MethodPost, "/assignments/asg-1:reoptimise", `{"assignmentVersion":5}`)
	if rec.Code != 200 || f.gotID != "asg-1" || f.gotVer != 5 {
		t.Fatalf("reoptimise = %d id=%s ver=%d", rec.Code, f.gotID, f.gotVer)
	}
	if rec := call(f, http.MethodPost, "/assignments/asg-2:reoptimise", ""); rec.Code != 200 || f.gotVer != 0 {
		t.Fatalf("an empty body is allowed: %d", rec.Code)
	}
}

func TestReoptimiseRejectsWhatIsNotTheAction(t *testing.T) {
	f := &fakeUC{}
	cases := map[string]struct {
		target, body string
		code         int
		slug         string
	}{
		"no action suffix": {"/assignments/asg-1", `{}`, 405, "method-not-allowed"},
		"other action":     {"/assignments/asg-1:cancel", `{}`, 405, "method-not-allowed"},
		"unknown path":     {"/nowhere", `{}`, 404, "resource-not-found"},
		"unknown field":    {"/assignments/asg-1:reoptimise", `{"nope":1}`, 400, "malformed-request"},
		"trailing data":    {"/assignments/asg-1:reoptimise", `{} {}`, 400, "malformed-request"},
		"bad json":         {"/assignments/asg-1:reoptimise", `{`, 400, "malformed-request"},
		"negative version": {"/assignments/asg-1:reoptimise", `{"assignmentVersion":-1}`, 422, "validation-failed"},
		"wrong type":       {"/assignments/asg-1:reoptimise", `{"assignmentVersion":"x"}`, 400, "malformed-request"},
	}
	for name, c := range cases {
		rec := call(f, http.MethodPost, c.target, c.body)
		if rec.Code != c.code || !strings.Contains(rec.Body.String(), `/`+c.slug+`"`) {
			t.Errorf("%s -> %d %s", name, rec.Code, rec.Body)
		}
	}
}

func TestEveryTypedErrorHasItsProblem(t *testing.T) {
	cases := []struct {
		err  error
		code int
		slug string
	}{
		{assignment.ErrAssignmentLocked, 409, "assignment-locked"},
		{assignment.ErrReleaseInFlight, 409, "release-in-flight"},
		{assignment.ErrNodeExcluded, 409, "node-excluded"},
		{assignment.ErrStaleAssignmentVersion, 409, "stale-assignment-version"},
		{assignment.ErrCandidateBreaksPromise, 422, "candidate-breaks-promise"},
		{ports.ErrNotFound, 404, "not-found"},
		{ports.ErrConcurrentUpdate, 409, "concurrent-update"},
		{shared.ErrIllegalTransition, 409, "illegal-transition"},
		{shared.Invalid("x"), 422, "validation-failed"},
		{fmt.Errorf("wrapped: %w", assignment.ErrAssignmentLocked), 409, "assignment-locked"},
		{errors.New("pq: password=hunter2"), 500, "internal-error"},
	}
	for _, c := range cases {
		rec := call(&fakeUC{err: c.err}, http.MethodGet, "/assignments/asg-1", "")
		var p problemDetail
		_ = json.Unmarshal(rec.Body.Bytes(), &p)
		if rec.Code != c.code || p.Type != problemBaseURI+c.slug || p.Status != c.code || p.Instance != "/assignments/asg-1" {
			t.Errorf("%v -> %d %+v", c.err, rec.Code, p)
		}
		if rec.Header().Get("Content-Type") != "application/problem+json" {
			t.Errorf("%v: content type %q", c.err, rec.Header().Get("Content-Type"))
		}
		if c.code == 500 && strings.Contains(p.Detail, "hunter2") {
			t.Error("an internal error must not leak its message")
		}
	}
}

func TestHealthz(t *testing.T) {
	rec := call(&fakeUC{}, http.MethodGet, "/healthz", "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"ok"`) {
		t.Fatalf("healthz = %d %s", rec.Code, rec.Body)
	}
}

func TestIdempotencyMiddlewareIsAppliedWhenConfigured(t *testing.T) {
	called := false
	h := (&Server{Reoptimise: &fakeUC{}, Idempotency: func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true; next.ServeHTTP(w, r) })
	}}).Handler()
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/assignments/asg-1:reoptimise", nil))
	if !called {
		t.Fatal("middleware not applied to the POST route")
	}
}

func TestIdempotencyHeaderValidation(t *testing.T) {
	mw := RequireIdempotencyKey(nil, nil)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("handler must not run") }))
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/assignments/a:reoptimise", nil))
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), "idempotency-key-required") {
		t.Fatalf("missing key = %d %s", rec.Code, rec.Body)
	}
	req := httptest.NewRequest(http.MethodPost, "/assignments/a:reoptimise", nil)
	req.Header.Set(IdempotencyKeyHeader, strings.Repeat("k", 129))
	rec = httptest.NewRecorder()
	mw.ServeHTTP(rec, req)
	if rec.Code != 422 || !strings.Contains(rec.Body.String(), "validation-failed") {
		t.Fatalf("long key = %d %s", rec.Code, rec.Body)
	}
}
