package features_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"time"

	"github.com/cucumber/godog"

	resthttp "github.com/claudioed/order-sourcing/internal/adapters/inbound/http"
	"github.com/claudioed/order-sourcing/internal/adapters/outbound/memory"
	"github.com/claudioed/order-sourcing/internal/application/usecases"
	"github.com/claudioed/order-sourcing/internal/application/views"
	"github.com/claudioed/order-sourcing/internal/domain/assignment"
)

// This file adds the planner's REST surface (apis/openapi.yaml), the booking
// facts (BookingMoved / BookingFailed, gap G14) and the virtual-queue sweep to
// the suite's vocabulary. The REST steps drive the REAL chi router of
// internal/adapters/inbound/http over the same in-memory doubles the command
// steps use, so every scenario crosses the adapter: DTO mapping, query parsing
// and the RFC 9457 problem catalogue included.
//
// The transactional Idempotency-Key middleware needs Postgres (advisory lock +
// stored outcome), so the BDD router runs without it; a second "keyed" router
// wires RequireIdempotencyKey with no pool, which is enough to prove the two
// deterministic rejections (missing key, over-long key) that are decided before
// any database access. The full replay contract is covered by the integration
// suite (idempotency_integration_test.go).

// restWorld is the per-scenario REST and sweep state. The suite runs scenarios
// sequentially, and every scenario re-registers this before its steps run.
type restWorld struct {
	handler http.Handler
	keyed   http.Handler
	status  int
	body    []byte
	sweep   usecases.SweepResult
}

var rest restWorld

const bookingPrefix = "com.retailnetwork.logistics.linehaul-transportation.departure."

func registerRestSteps(sc *godog.ScenarioContext, w *world) {
	sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
		queries := usecases.NewQueries(w.deps.Assignments)
		reopt := usecases.NewReoptimiseAssignment(w.deps)
		rest = restWorld{
			handler: (&resthttp.Server{List: queries, Get: queries, Reoptimise: reopt}).Handler(),
			keyed:   (&resthttp.Server{Reoptimise: reopt, Idempotency: resthttp.RequireIdempotencyKey(nil, nil)}).Handler(),
		}
		return ctx, nil
	})

	registerRequestSteps(sc, w)
	registerResponseSteps(sc)
	registerBookingAndSweepSteps(sc, w)
}

func (r *restWorld) do(h http.Handler, method, path, body string) error {
	var reader *strings.Reader
	if body == "" {
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	r.status, r.body = rec.Code, rec.Body.Bytes()
	return nil
}

func registerRequestSteps(sc *godog.ScenarioContext, w *world) {
	sc.Step(`^a (GET|POST) request is sent to "([^"]*)"$`, func(method, path string) error {
		return rest.do(rest.handler, method, path, "")
	})
	sc.Step(`^a (GET|POST) request is sent to "([^"]*)" with the raw body (.+)$`, func(method, path, body string) error {
		return rest.do(rest.handler, method, path, body)
	})
	sc.Step(`^a GET request is sent to the assignment of "([^"]*)" group (\d+)$`, func(order string, group int) error {
		return w.withIDRaw(order, group, func(id, path string) error { return rest.do(rest.handler, http.MethodGet, path, "") })
	})
	sc.Step(`^a reoptimise request is posted for the assignment of "([^"]*)" group (\d+)$`, func(order string, group int) error {
		return w.withIDRaw(order, group, func(id, path string) error { return rest.do(rest.handler, http.MethodPost, path+":reoptimise", "") })
	})
	sc.Step(`^a reoptimise request is posted for the assignment of "([^"]*)" group (\d+) with the JSON body (.+)$`, func(order string, group int, body string) error {
		return w.withIDRaw(order, group, func(id, path string) error { return rest.do(rest.handler, http.MethodPost, path+":reoptimise", body) })
	})
	sc.Step(`^a reoptimise request for the assignment of "([^"]*)" group (\d+) is sent without an Idempotency-Key$`, func(order string, group int) error {
		return w.withIDRaw(order, group, func(id, path string) error { return rest.do(rest.keyed, http.MethodPost, path+":reoptimise", "") })
	})
	sc.Step(`^a reoptimise request for the assignment of "([^"]*)" group (\d+) is sent with a 129-character Idempotency-Key$`, func(order string, group int) error {
		return w.withIDRaw(order, group, func(id, path string) error {
			req := httptest.NewRequest(http.MethodPost, path+":reoptimise", strings.NewReader(""))
			req.Header.Set(resthttp.IdempotencyKeyHeader, strings.Repeat("k", 129))
			rec := httptest.NewRecorder()
			rest.keyed.ServeHTTP(rec, req)
			rest.status, rest.body = rec.Code, rec.Body.Bytes()
			return nil
		})
	})
}

func registerResponseSteps(sc *godog.ScenarioContext) {
	sc.Step(`^the response status is (\d+)$`, rest.wantStatus)
	sc.Step(`^the response problem type is "([^"]*)"$`, rest.wantProblemType)
	sc.Step(`^the response field "([^"]*)" is "([^"]*)"$`, rest.wantField)
	sc.Step(`^the response has no field "([^"]*)"$`, rest.wantNoField)
	sc.Step(`^the response list "([^"]*)" has (\d+) items?$`, rest.wantListLen)
	sc.Step(`^the response list "([^"]*)" contains an item whose "([^"]*)" is "([^"]*)"$`, rest.wantListItem)
}

func (r *restWorld) wantStatus(code int) error {
	if r.status != code {
		return fmt.Errorf("the response status is %d, want %d (%s)", r.status, code, r.body)
	}
	return nil
}

func (r *restWorld) wantProblemType(slug string) error {
	var p struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(r.body, &p); err != nil {
		return fmt.Errorf("the response is not a problem document: %w (%s)", err, r.body)
	}
	if !strings.HasSuffix(p.Type, "/"+slug) {
		return fmt.Errorf("the problem type is %q, want it to end with %q", p.Type, slug)
	}
	return nil
}

func (r *restWorld) wantField(path, want string) error {
	got, err := r.at(path)
	if err != nil {
		return err
	}
	if s := fmt.Sprint(got); s != want {
		return fmt.Errorf("the response field %q is %q, want %q", path, s, want)
	}
	return nil
}

func (r *restWorld) wantNoField(path string) error {
	if _, err := r.at(path); err == nil {
		return fmt.Errorf("the response has a field %q, want it absent", path)
	}
	return nil
}

func (r *restWorld) wantListLen(path string, n int) error {
	list, err := r.listAt(path)
	if err != nil {
		return err
	}
	if len(list) != n {
		return fmt.Errorf("the response list %q has %d items, want %d", path, len(list), n)
	}
	return nil
}

func (r *restWorld) wantListItem(listPath, field, want string) error {
	list, err := r.listAt(listPath)
	if err != nil {
		return err
	}
	for _, item := range list {
		if v, err := walk(item, field); err == nil && fmt.Sprint(v) == want {
			return nil
		}
	}
	return fmt.Errorf("no item of %q has %s = %q", listPath, field, want)
}

// listAt decodes the list at a dotted path of the last response.
func (r *restWorld) listAt(path string) ([]any, error) {
	got, err := r.at(path)
	if err != nil {
		return nil, err
	}
	list, ok := got.([]any)
	if !ok {
		return nil, fmt.Errorf("the response field %q is %v, want a list", path, got)
	}
	return list, nil
}

// walk descends a dotted path from an already-decoded JSON value.
func walk(cur any, path string) (any, error) {
	for _, seg := range strings.Split(path, ".") {
		switch node := cur.(type) {
		case map[string]any:
			v, ok := node[seg]
			if !ok {
				return nil, fmt.Errorf("no field %q at %q", seg, path)
			}
			cur = v
		case []any:
			i, err := strconv.Atoi(seg)
			if err != nil || i < 0 || i >= len(node) {
				return nil, fmt.Errorf("%q is not an index into a list of %d items", seg, len(node))
			}
			cur = node[i]
		default:
			return nil, fmt.Errorf("cannot walk into %q: %v is a scalar", seg, cur)
		}
	}
	return cur, nil
}

// at walks a dotted path ("assignments.0.candidate.nodeId") into the decoded
// last response body.
func (r *restWorld) at(path string) (any, error) {
	var doc any
	if err := json.Unmarshal(r.body, &doc); err != nil {
		return nil, fmt.Errorf("the response is not JSON: %w (%s)", err, r.body)
	}
	return walk(doc, path)
}

func registerBookingAndSweepSteps(sc *godog.ScenarioContext, w *world) {
	sc.Step(`^the booking of "([^"]*)" group (\d+) moves from departure "([^"]*)" to "([^"]*)"$`, func(order string, group int, from, to string) error {
		return w.withID(order, group, func(id assignment.ID) error {
			_, err := usecases.NewRecordPickupMoved(w.deps).Execute(w.ctx(), usecases.RecordPickupMovedCommand{
				Meta:         usecases.Meta{EventID: "evt-" + order + "-moved", EventType: bookingPrefix + "BookingMoved"},
				AssignmentID: id, FromDeparture: from, ToDeparture: to})
			return err
		})
	})
	sc.Step(`^the assignment of "([^"]*)" group (\d+) follows the pickup departure "([^"]*)"$`, func(order string, group int, departure string) error {
		return w.check(order, group, func(v views.Assignment) error {
			if v.PickupDepartureID != departure {
				return fmt.Errorf("pickup departure is %q, want %q", v.PickupDepartureID, departure)
			}
			if v.Candidate == nil || v.Candidate.PickupDepartureID != departure {
				return fmt.Errorf("the candidate must follow the move too, candidate is %+v", v.Candidate)
			}
			return nil
		})
	})
	sc.Step(`^event "([^"]*)" is recorded as processed$`, func(event string) error {
		w.run(func() error {
			seen, err := usecases.NewRecordProcessed(w.deps).Execute(w.ctx(), usecases.Meta{EventID: event, EventType: bookingPrefix + "BookingFailed"})
			if err == nil && !seen {
				err = fmt.Errorf("the first delivery of %s must be recorded as newly processed", event)
			}
			return err
		})
		return nil
	})
	sc.Step(`^event "([^"]*)" is recorded as processed again$`, func(event string) error {
		w.run(func() error {
			seen, err := usecases.NewRecordProcessed(w.deps).Execute(w.ctx(), usecases.Meta{EventID: event, EventType: bookingPrefix + "BookingFailed"})
			if err == nil && seen {
				err = fmt.Errorf("the redelivery of %s must not be recorded as newly processed", event)
			}
			return err
		})
		return nil
	})
	sc.Step(`^the clock is advanced by (\d+) hours$`, func(n int) error {
		c, ok := w.deps.Clock.(*memory.Clock)
		if !ok {
			return fmt.Errorf("the scenario clock is %T, not the settable double", w.deps.Clock)
		}
		c.Advance(time.Duration(n) * time.Hour)
		return nil
	})
	sc.Step(`^the sweep runs$`, func() error {
		w.run(func() error {
			res, err := usecases.NewSweepQueue(w.deps, usecases.NewReleaseToNode(w.deps), usecases.NewReoptimiseAssignment(w.deps)).Execute(w.ctx(), "", 10)
			rest.sweep = res
			return err
		})
		return nil
	})
	sc.Step(`^the sweep released (\d+) assignments?$`, rest.sweepCounter("released", func() int { return rest.sweep.Released }))
	sc.Step(`^the sweep reoptimised (\d+) assignments?$`, rest.sweepCounter("reoptimised", func() int { return rest.sweep.Reoptimised }))
	sc.Step(`^the sweep skipped (\d+) assignments?$`, rest.sweepCounter("skipped", func() int { return rest.sweep.Skipped }))
}

func (r *restWorld) sweepCounter(what string, value func() int) func(int) error {
	return func(want int) error {
		if got := value(); got != want {
			return fmt.Errorf("the sweep %s %d assignments, want %d (result %+v)", what, got, want, r.sweep)
		}
		return nil
	}
}

// withIDRaw resolves the assignment of one group and hands the step the id and
// its REST path.
func (w *world) withIDRaw(order string, group int, run func(id, path string) error) error {
	v, err := w.assignmentOf(order, group)
	if err != nil {
		return err
	}
	w.run(func() error { return run(v.AssignmentID, "/assignments/"+v.AssignmentID) })
	return nil
}
