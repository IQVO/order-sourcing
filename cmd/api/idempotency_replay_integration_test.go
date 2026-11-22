//go:build integration

package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/claudioed/order-sourcing/internal/adapters/outbound/postgres"
	"github.com/claudioed/order-sourcing/internal/application/ports"
	"github.com/claudioed/order-sourcing/internal/application/usecases"
	"github.com/claudioed/order-sourcing/internal/domain/assignment"
)

// ADR 0019 (replay classes) on the REAL router, the REAL use cases and a real Postgres: what a
// retry under the same Idempotency-Key gets after each class of refusal. Only the repository's
// Get is decorated, to lose a race the way a concurrent writer does (a committed version bump on
// another connection between the load and the save), and to hold the first request in flight.

// racingRepo makes the next `losses` loads lose the race to another writer, and holds the first
// load on `block` when it is set.
type racingRepo struct {
	ports.AssignmentRepository
	pool   *pgxpool.Pool
	losses atomic.Int32
	mu     sync.Mutex
	block  chan struct{}
}

func (r *racingRepo) holdNext() chan struct{} {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.block = make(chan struct{})
	return r.block
}

func (r *racingRepo) takeBlock() chan struct{} {
	r.mu.Lock()
	defer r.mu.Unlock()
	ch := r.block
	r.block = nil
	return ch
}

func (r *racingRepo) Get(ctx context.Context, id assignment.ID) (*assignment.Assignment, error) {
	a, err := r.AssignmentRepository.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if ch := r.takeBlock(); ch != nil {
		<-ch
	}
	if r.losses.Load() > 0 {
		r.losses.Add(-1)
		if _, err := r.pool.Exec(ctx, `UPDATE assignments SET version = version + 1 WHERE assignment_id = $1`, string(id)); err != nil {
			return nil, err
		}
	}
	return a, nil
}

type replayWorld struct {
	t     *testing.T
	pool  *pgxpool.Pool
	repo  *racingRepo
	svc   services
	api   api
	store ports.AssignmentRepository
}

func newReplayWorld(t *testing.T) *replayWorld {
	t.Helper()
	url := startPostgres(t)
	if err := postgres.RunMigrations(url); err != nil {
		t.Fatalf("migrations: %v", err)
	}
	pool, err := postgres.NewPool(context.Background(), url)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)
	svc := buildServices(pool, nil, nil, nil, nil)
	repo := &racingRepo{AssignmentRepository: svc.deps.Assignments, pool: pool}
	deps := svc.deps
	deps.Assignments = repo
	svc.server.Reoptimise = usecases.NewReoptimiseAssignment(deps)
	srv := httptest.NewServer(svc.server.Handler())
	t.Cleanup(srv.Close)
	return &replayWorld{t: t, pool: pool, repo: repo, svc: svc, api: api{t: t, base: srv.URL}, store: svc.deps.Assignments}
}

var base = time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)

const (
	stageQueued = iota
	stageReleased
	stageLocked
)

func (w *replayWorld) seed(id string, stage int) {
	w.t.Helper()
	in := assignment.New{
		ID: assignment.ID(id), OrderID: "ord-" + id, GroupNo: 1, DestinationPostalPrefix: "30301",
		Promise: assignment.PromiseConstraint{PromiseID: "prm-" + id, Tier: assignment.TierTwoDay, DeliverByLatest: base.Add(60 * time.Hour), WitnessNodeID: "node-a"},
		Lines:   []assignment.Line{{LineNo: 1, CatalogItemID: "cat-1", Quantity: 2}},
	}
	cand := &assignment.Candidate{
		NodeID: "node-a", PickupDepartureID: "dep-a", PickupCutoffAt: base.Add(5 * time.Hour), Legs: []string{"leg-1"},
		DeliveryWaveID: "wave-1", ETA: base.Add(30 * time.Hour), Cost: 130, EstimatedCostCents: 950, CostComplete: true,
	}
	a, _, err := assignment.Queue(in, cand, base.Add(2*time.Hour), base)
	if err != nil {
		w.t.Fatalf("queue %s: %v", id, err)
	}
	if err := w.store.Save(context.Background(), a); err != nil {
		w.t.Fatalf("save %s: %v", id, err)
	}
	if stage == stageQueued {
		return
	}
	lines := []assignment.Line{{LineNo: 1, CatalogItemID: "cat-1", ListingID: "lst-1", OwnerRef: "sel-1", Quantity: 2}}
	facts := assignment.SourcingFacts{NodeRegionID: "r1", DestinationRegionID: "r1", CandidateCount: 1}
	if _, err := a.Release(cand, lines, facts, base.Add(2*time.Hour)); err != nil {
		w.t.Fatalf("release %s: %v", id, err)
	}
	if err := w.store.Save(context.Background(), a); err != nil {
		w.t.Fatalf("save released %s: %v", id, err)
	}
	if stage == stageLocked {
		if _, err := a.Lock(1, base.Add(3*time.Hour), base.Add(3*time.Hour)); err != nil {
			w.t.Fatalf("lock %s: %v", id, err)
		}
		if err := w.store.Save(context.Background(), a); err != nil {
			w.t.Fatalf("save locked %s: %v", id, err)
		}
	}
}

func (w *replayWorld) count(sql string, args ...any) int {
	w.t.Helper()
	var n int
	if err := w.pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		w.t.Fatalf("%s: %v", sql, err)
	}
	return n
}

func (w *replayWorld) keyRows(key string) int {
	return w.count(`SELECT count(*) FROM idempotency_keys WHERE key = $1`, key)
}

func (w *replayWorld) reoptimise(id, key, body string) (int, http.Header, string) {
	w.t.Helper()
	code, h, raw := w.api.do("POST", "/assignments/"+id+"/reoptimisations", key, body)
	return code, h, string(raw)
}

func slugOf(t *testing.T, body string) string {
	t.Helper()
	var p struct{ Type string }
	if err := json.Unmarshal([]byte(body), &p); err != nil {
		t.Fatalf("not a problem body %q: %v", body, err)
	}
	return p.Type[strings.LastIndex(p.Type, "/")+1:]
}

func replayed(h http.Header) bool { return h.Get("Idempotency-Replayed") == "true" }

func TestAnOptimisticLockLossIsNotStoredAndItsKeyRetriesTheWork(t *testing.T) {
	w := newReplayWorld(t)
	w.seed("asg-race", stageQueued)
	w.repo.losses.Store(2) // the load, then the in-use-case retry on fresh state: both lose

	code, h, body := w.reoptimise("asg-race", "k-race", `{}`)
	if code != 409 || slugOf(t, body) != "concurrent-update" || replayed(h) {
		t.Fatalf("the loser = %d %s, want 409 concurrent-update", code, body)
	}
	if n := w.keyRows("k-race"); n != 0 {
		t.Fatalf("the loser's key row count = %d, want 0: a RETRYABLE refusal releases the key", n)
	}
	code, h, first := w.reoptimise("asg-race", "k-race", `{}`)
	if code != 200 || replayed(h) {
		t.Fatalf("the retry under the same key = %d replayed=%v %s, want a fresh 200 that executed", code, replayed(h), first)
	}
	if n := w.keyRows("k-race"); n != 1 {
		t.Fatalf("key rows after the retry = %d, want 1: a 2xx is stored", n)
	}
	versionBefore := w.count(`SELECT version FROM assignments WHERE assignment_id = 'asg-race'`)
	code, h, again := w.reoptimise("asg-race", "k-race", `{}`)
	if code != 200 || !replayed(h) || again != first {
		t.Fatalf("third request = %d replayed=%v, want the stored 200 replayed byte for byte", code, replayed(h))
	}
	if v := w.count(`SELECT version FROM assignments WHERE assignment_id = 'asg-race'`); v != versionBefore {
		t.Fatalf("the replay moved the assignment from version %d to %d", versionBefore, v)
	}
}

func TestANotFoundIsNotStoredSoTheKeyWorksOnceTheEventArrives(t *testing.T) {
	w := newReplayWorld(t)
	code, _, body := w.reoptimise("asg-late", "k-late", `{}`)
	if code != 404 || slugOf(t, body) != "not-found" || w.keyRows("k-late") != 0 {
		t.Fatalf("unknown assignment = %d %s rows=%d, want 404 not-found and no key row", code, body, w.keyRows("k-late"))
	}
	// The released key is free: another body is a NEW request, not idempotency-key-reused.
	if code, _, body := w.reoptimise("asg-late", "k-late", `{"assignmentVersion":0}`); code != 404 || strings.Contains(body, "idempotency-key-reused") {
		t.Fatalf("another body under the released key = %d %s, want a new request (404 again)", code, body)
	}
	w.seed("asg-late", stageQueued) // CustomerOrderPlaced: the assignment arrives from the event
	code, h, first := w.reoptimise("asg-late", "k-late", `{}`)
	if code != 200 || replayed(h) || w.keyRows("k-late") != 1 {
		t.Fatalf("after the event = %d replayed=%v rows=%d %s, want a fresh stored 200", code, replayed(h), w.keyRows("k-late"), first)
	}
	if code, h, again := w.reoptimise("asg-late", "k-late", `{}`); code != 200 || !replayed(h) || again != first {
		t.Fatalf("replay = %d replayed=%v, want the stored 200", code, replayed(h))
	}
}

func TestAStateRefusalLeavesNoTraceAndIsNotStored(t *testing.T) {
	w := newReplayWorld(t)
	w.seed("asg-rel", stageReleased)
	versionBefore := w.count(`SELECT version FROM assignments WHERE assignment_id = 'asg-rel'`)
	outboxBefore := w.count(`SELECT count(*) FROM outbox_events`)

	code, _, body := w.reoptimise("asg-rel", "k-rel", `{}`)
	if code != 409 || slugOf(t, body) != "release-in-flight" || w.keyRows("k-rel") != 0 {
		t.Fatalf("a RELEASED assignment = %d %s rows=%d, want 409 release-in-flight and no key row", code, body, w.keyRows("k-rel"))
	}
	if v := w.count(`SELECT version FROM assignments WHERE assignment_id = 'asg-rel'`); v != versionBefore {
		t.Fatalf("the refusal moved the assignment from version %d to %d", versionBefore, v)
	}
	if n := w.count(`SELECT count(*) FROM outbox_events`); n != outboxBefore {
		t.Fatalf("the refusal appended %d outbox rows", n-outboxBefore)
	}
	if code, h, _ := w.reoptimise("asg-rel", "k-rel", `{}`); code != 409 || replayed(h) {
		t.Fatalf("the retry = %d replayed=%v, want the refusal executed again, not replayed", code, replayed(h))
	}
}

func TestALockedAssignmentRefusalIsFinalSoItIsStoredAndReplayed(t *testing.T) {
	w := newReplayWorld(t)
	w.seed("asg-lock", stageLocked)
	code, _, first := w.reoptimise("asg-lock", "k-lock", `{}`)
	if code != 409 || slugOf(t, first) != "assignment-locked" || w.keyRows("k-lock") != 1 {
		t.Fatalf("a LOCKED assignment = %d %s rows=%d, want 409 assignment-locked stored", code, first, w.keyRows("k-lock"))
	}
	if code, h, again := w.reoptimise("asg-lock", "k-lock", `{}`); code != 409 || !replayed(h) || again != first {
		t.Fatalf("replay = %d replayed=%v, want the stored 409", code, replayed(h))
	}
}

func TestAMalformedRequestIsFinalSoItIsStoredAndReplayed(t *testing.T) {
	w := newReplayWorld(t)
	code, _, first := w.reoptimise("asg-any", "k-bad", `{"nope":1}`)
	if code != 400 || slugOf(t, first) != "malformed-request" || w.keyRows("k-bad") != 1 {
		t.Fatalf("unknown field = %d %s rows=%d, want 400 malformed-request stored", code, first, w.keyRows("k-bad"))
	}
	code, h, again := w.reoptimise("asg-any", "k-bad", `{"nope":1}`)
	if code != 400 || !replayed(h) || again != first {
		t.Fatalf("replay = %d replayed=%v, want the stored 400", code, replayed(h))
	}
	if code, _, body := w.reoptimise("asg-any", "k-bad", `{"nope":2}`); code != 409 || slugOf(t, body) != "idempotency-key-reused" {
		t.Fatalf("another body under a stored key = %d %s, want 409 idempotency-key-reused", code, body)
	}
}

func TestAValidationRefusalOfThePolicyRouteIsNotStored(t *testing.T) {
	w := newReplayWorld(t)
	put := func(key, body string) (int, string) {
		code, _, raw := w.api.do("PUT", "/sourcing-policy", key, body)
		return code, string(raw)
	}
	if code, body := put("k-pol", `{"maxCostDeviationPercent":201}`); code != 422 || slugOf(t, body) != "validation-failed" || w.keyRows("k-pol") != 0 {
		t.Fatalf("out of range = %d %s rows=%d, want 422 validation-failed and no key row", code, body, w.keyRows("k-pol"))
	}
	if code, body := put("k-pol", `{"maxCostDeviationPercent":20}`); code != 200 || !strings.Contains(body, `"version":1`) || w.keyRows("k-pol") != 1 {
		t.Fatalf("a valid body under the released key = %d %s, want version 1 stored", code, body)
	}
}

func TestAConcurrentDuplicateOfARetryableRefusalRunsTheWorkItself(t *testing.T) {
	w := newReplayWorld(t)
	w.seed("asg-dup", stageQueued)
	w.repo.losses.Store(2)
	release := w.repo.holdNext()

	type result struct {
		code int
		h    http.Header
		body string
	}
	out := make(chan result, 2)
	run := func() {
		code, h, body := w.reoptimise("asg-dup", "k-dup", `{}`)
		out <- result{code, h, body}
	}
	go run() // the first request holds the key's advisory lock and waits in the load
	deadline := time.Now().Add(20 * time.Second)
	for w.count(`SELECT count(*) FROM pg_locks WHERE locktype = 'advisory' AND granted`) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the first request never took the key's lock")
		}
		time.Sleep(20 * time.Millisecond)
	}
	go run() // the duplicate: it waits on the same advisory lock
	for w.count(`SELECT count(*) FROM pg_locks WHERE locktype = 'advisory' AND NOT granted`) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the duplicate never waited on the key's lock")
		}
		time.Sleep(20 * time.Millisecond)
	}
	close(release)

	first, second := <-out, <-out
	if first.code != 409 { // the answers arrive in either order: the refused one is the first request
		first, second = second, first
	}
	if first.code != 409 || slugOf(t, first.body) != "concurrent-update" {
		t.Fatalf("the first request = %d %s, want 409 concurrent-update", first.code, first.body)
	}
	if second.code != 200 || replayed(second.h) {
		t.Fatalf("the duplicate = %d replayed=%v %s, want a fresh 200: it found no stored row and ran the work", second.code, replayed(second.h), second.body)
	}
	if n := w.keyRows("k-dup"); n != 1 {
		t.Fatalf("key rows = %d, want 1 (the duplicate's 200)", n)
	}
}
