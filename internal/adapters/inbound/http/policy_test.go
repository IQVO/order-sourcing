package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/claudioed/order-sourcing/internal/application/ports"
	"github.com/claudioed/order-sourcing/internal/application/views"
	"github.com/claudioed/order-sourcing/internal/domain/assignment"
	"github.com/claudioed/order-sourcing/internal/domain/policy"
	"github.com/claudioed/order-sourcing/internal/domain/shared"
)

// fakePolicy is both use cases of /sourcing-policy.
type fakePolicy struct {
	cur     views.SourcingPolicy
	err     error
	got     policy.Input
	publish int
}

func (f *fakePolicy) Current(context.Context) (views.SourcingPolicy, error) { return f.cur, f.err }

func (f *fakePolicy) Publish(_ context.Context, in policy.Input) (views.SourcingPolicy, error) {
	f.publish++
	f.got = in
	if f.err != nil {
		return views.SourcingPolicy{}, f.err
	}
	return views.SourcingPolicy{
		Version: f.cur.Version + 1, CrossRegionSurchargeCents: in.CrossRegionSurchargeCents, MaxCostDeviationPercent: in.MaxCostDeviationPercent, PublishedAt: t0,
	}, nil
}

func callPolicy(f *fakePolicy, method, body string) *httptest.ResponseRecorder {
	h := (&Server{PolicyGet: f, PolicyPut: f}).Handler()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, "/sourcing-policy", strings.NewReader(body)))
	return rec
}

func decodeMap(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("body %q: %v", rec.Body, err)
	}
	return out
}

func TestGetSourcingPolicyDefaultsAreVersionZeroWithoutPublishedAt(t *testing.T) {
	rec := callPolicy(&fakePolicy{}, http.MethodGet, "")
	got := decodeMap(t, rec)
	if rec.Code != 200 || got["version"] != 0.0 || got["crossRegionSurchargeCents"] != 0.0 || got["maxCostDeviationPercent"] != 0.0 {
		t.Fatalf("defaults = %d %s", rec.Code, rec.Body)
	}
	if _, ok := got["publishedAt"]; ok {
		t.Fatalf("version 0 has no publishedAt: %s", rec.Body)
	}
	if rec.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("content type %q", rec.Header().Get("Content-Type"))
	}
}

func TestGetSourcingPolicyReturnsTheCurrentVersion(t *testing.T) {
	rec := callPolicy(&fakePolicy{cur: views.SourcingPolicy{Version: 3, CrossRegionSurchargeCents: 250, MaxCostDeviationPercent: 20, PublishedAt: t0}}, http.MethodGet, "")
	got := decodeMap(t, rec)
	if rec.Code != 200 || got["version"] != 3.0 || got["crossRegionSurchargeCents"] != 250.0 || got["maxCostDeviationPercent"] != 20.0 || got["publishedAt"] != "2026-10-01T09:00:00Z" {
		t.Fatalf("policy = %d %s", rec.Code, rec.Body)
	}
}

func TestPutSourcingPolicyPublishesTheNextVersion(t *testing.T) {
	f := &fakePolicy{cur: views.SourcingPolicy{Version: 3}}
	rec := callPolicy(f, http.MethodPut, `{"crossRegionSurchargeCents":250,"maxCostDeviationPercent":20}`)
	got := decodeMap(t, rec)
	if rec.Code != 200 || got["version"] != 4.0 || got["crossRegionSurchargeCents"] != 250.0 || got["maxCostDeviationPercent"] != 20.0 {
		t.Fatalf("publish = %d %s", rec.Code, rec.Body)
	}
	if f.got.CrossRegionSurchargeCents != 250 || f.got.MaxCostDeviationPercent != 20 {
		t.Fatalf("input = %+v", f.got)
	}
	// An omitted lever is its default, 0: the document is replaced as a whole.
	callPolicy(f, http.MethodPut, `{"maxCostDeviationPercent":5}`)
	if f.got.CrossRegionSurchargeCents != 0 || f.got.MaxCostDeviationPercent != 5 {
		t.Fatalf("input = %+v", f.got)
	}
}

func TestPutSourcingPolicyRefusesAMalformedBody(t *testing.T) {
	for name, body := range map[string]string{
		"empty body":         "",
		"not json":           "x",
		"unknown field":      `{"splitSurchargeCents":5}`,
		"a string number":    `{"maxCostDeviationPercent":"20"}`,
		"a fractional value": `{"crossRegionSurchargeCents":1.5}`,
		"trailing data":      `{} {}`,
	} {
		f := &fakePolicy{}
		rec := callPolicy(f, http.MethodPut, body)
		if rec.Code != 400 || !strings.Contains(rec.Body.String(), "malformed-request") || f.publish != 0 {
			t.Errorf("%s -> %d %s (published %d)", name, rec.Code, rec.Body, f.publish)
		}
		if rec.Header().Get("Content-Type") != "application/problem+json" {
			t.Errorf("%s: content type %q", name, rec.Header().Get("Content-Type"))
		}
	}
}

func TestPutSourcingPolicyMapsTheDomainErrorsToProblems(t *testing.T) {
	cases := []struct {
		err  error
		code int
		slug string
	}{
		{shared.Invalid("crossRegionSurchargeCents must be between 0 and 100000"), 422, "validation-failed"},
		{ports.ErrConcurrentUpdate, 409, "concurrent-update"},
	}
	for _, c := range cases {
		rec := callPolicy(&fakePolicy{err: c.err}, http.MethodPut, `{"crossRegionSurchargeCents":100001}`)
		var p problemDetail
		_ = json.Unmarshal(rec.Body.Bytes(), &p)
		if rec.Code != c.code || p.Type != problemBaseURI+c.slug || p.Instance != "/sourcing-policy" || rec.Header().Get("Content-Type") != "application/problem+json" {
			t.Errorf("%v -> %d %+v", c.err, rec.Code, p)
		}
	}
}

func TestSourcingPolicyRouteAllowsOnlyGetAndPut(t *testing.T) {
	for _, m := range []string{http.MethodPost, http.MethodDelete, http.MethodPatch} {
		rec := callPolicy(&fakePolicy{}, m, "{}")
		if rec.Code != 405 || rec.Header().Get("Content-Type") != "application/problem+json" {
			t.Errorf("%s -> %d %s", m, rec.Code, rec.Body)
		}
	}
}

// The REST candidate view carries the policy fields under the release's
// omission rules (ADR 0014).
func TestCandidateViewCarriesThePolicyFieldsOnlyWhenTheyApply(t *testing.T) {
	candidate := func(c views.Candidate) map[string]any {
		v := sample()
		v.Candidate = &c
		return decodeMap(t, callFor(v))["candidate"].(map[string]any)
	}
	plain := candidate(views.Candidate{NodeID: "n1", ETA: t0, PickupCutoffAt: t0, EstimatedCostCents: 900, CostComplete: true})
	for _, k := range []string{"policyVersion", "effectiveCostCents"} {
		if _, ok := plain[k]; ok {
			t.Errorf("%s must be omitted without a policy: %v", k, plain)
		}
	}
	versioned := candidate(views.Candidate{NodeID: "n1", ETA: t0, PickupCutoffAt: t0, EstimatedCostCents: 900, CostComplete: true, PolicyVersion: 3})
	if versioned["policyVersion"] != 3.0 || versioned["effectiveCostCents"] != nil {
		t.Errorf("a policy without a surcharge: %v", versioned)
	}
	charged := candidate(views.Candidate{NodeID: "n1", ETA: t0, PickupCutoffAt: t0, EstimatedCostCents: 900, CostComplete: true, PolicyVersion: 3, SurchargeCents: 300})
	if charged["policyVersion"] != 3.0 || charged["effectiveCostCents"] != 1200.0 || charged["estimatedCostCents"] != 900.0 {
		t.Errorf("a surcharge: %v", charged)
	}
	incomplete := candidate(views.Candidate{NodeID: "n1", ETA: t0, PickupCutoffAt: t0, PolicyVersion: 3, SurchargeCents: 300})
	if _, ok := incomplete["estimatedCostCents"]; ok || incomplete["effectiveCostCents"] != 300.0 {
		t.Errorf("an incomplete estimate still reports the effective cost: %v", incomplete)
	}
}

func callFor(v views.Assignment) *httptest.ResponseRecorder {
	f := &fakeUC{list: []views.Assignment{v}}
	h := (&Server{List: f, Get: &getOne{v}, Reoptimise: f}).Handler()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/assignments/asg-1", nil))
	return rec
}

// getOne is a Getter that returns a fixed view.
type getOne struct{ v views.Assignment }

func (g *getOne) GetAssignment(context.Context, assignment.ID) (views.Assignment, error) {
	return g.v, nil
}
