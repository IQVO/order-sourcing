package http

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/claudioed/order-sourcing/internal/application/ports"
)

// The replay classes (ADR 0019): every problem type declares what a retry under the same
// Idempotency-Key gets, and the middleware acts on the class and on nothing else. These tests
// need no database: the middleware's choice is driven over a spy transaction; the Postgres half
// is idempotency_replay_classes_integration_test.go and cmd/api/idempotency_replay_integration_test.go.

func TestEveryCatalogueEntryDeclaresAReplayClass(t *testing.T) {
	if len(problemCatalogue) == 0 {
		t.Fatal("the catalogue is empty: the test would pass on nothing")
	}
	classOf := map[string]replayClass{}
	for _, e := range problemCatalogue {
		t.Run(fmt.Sprintf("%s/%d", e.p.slug, e.p.status), func(t *testing.T) {
			if !e.replay.declared() {
				t.Errorf("problem type %q (%d) declares no replay class (%s): set FINAL, OUTCOME or RETRYABLE on its catalogue entry (.claude/rules/fleet/idempotency-and-outbox.md)", e.p.slug, e.p.status, e.replay)
			}
			if e.p.status >= http.StatusInternalServerError && e.replay != replayRetryable {
				t.Errorf("problem type %q answers %d: a 5xx is never stored, so its class is RETRYABLE, got %s", e.p.slug, e.p.status, e.replay)
			}
			if prev, seen := classOf[e.p.slug]; seen && prev != e.replay {
				t.Errorf("slug %q is declared %s and %s: one problem type has exactly one class", e.p.slug, prev, e.replay)
			}
			classOf[e.p.slug] = e.replay
		})
	}
}

func TestTheReplayClassCheckBites(t *testing.T) {
	for _, c := range []replayClass{replayUnset, replayClass(4), replayClass(255)} {
		if c.declared() {
			t.Errorf("class %d is accepted; only FINAL, OUTCOME and RETRYABLE are classes", c)
		}
	}
	for c, name := range map[replayClass]string{replayFinal: "FINAL", replayOutcome: "OUTCOME", replayRetryable: "RETRYABLE"} {
		if !c.declared() || c.String() != name {
			t.Errorf("class %d = %s declared=%v, want %s declared", c, c, c.declared(), name)
		}
	}
	if replayUnset.String() != "UNSET" {
		t.Errorf("the zero class reads %q, want UNSET", replayUnset)
	}
}

// writeError is the one production error writer: it must tell the capturing writer the class of
// the entry it answers with, and nothing for a 5xx.
func TestWriteErrorMarksTheCaptureWithTheClassOfItsEntry(t *testing.T) {
	for _, e := range problemCatalogue {
		rec := &capture{header: http.Header{}}
		writeError(rec, httptest.NewRequest(http.MethodPost, "/assignments/a/reoptimisations", nil), fmt.Errorf("%w: detail", e.err))
		if rec.class != e.replay || rec.code != e.p.status {
			t.Errorf("%s: capture holds %s/%d, want %s/%d", e.p.slug, rec.class, rec.code, e.replay, e.p.status)
		}
	}
	rec := &capture{header: http.Header{}}
	writeError(rec, httptest.NewRequest(http.MethodPost, "/assignments/a/reoptimisations", nil), errors.New("disk on fire"))
	if rec.class != replayUnset || rec.code != http.StatusInternalServerError {
		t.Errorf("an unmapped error is %s/%d, want UNSET/500", rec.class, rec.code)
	}
	plain := httptest.NewRecorder() // a writer that is not the capture: no marking, no panic
	writeError(plain, httptest.NewRequest(http.MethodGet, "/assignments/a", nil), ports.ErrNotFound)
	if plain.Code != http.StatusNotFound {
		t.Errorf("a plain writer answered %d, want 404", plain.Code)
	}
}

func TestTheVerdictReadsTheStatusFamilyAndTheClassOnly(t *testing.T) {
	for name, c := range map[string]struct {
		code  int
		class replayClass
		want  verdict
	}{
		"200 is stored with its writes":          {http.StatusOK, replayUnset, storeAndKeep},
		"201 is stored with its writes":          {http.StatusCreated, replayUnset, storeAndKeep},
		"a FINAL 400 is stored alone":            {http.StatusBadRequest, replayFinal, storeRefusal},
		"a FINAL 422 is stored alone":            {http.StatusUnprocessableEntity, replayFinal, storeRefusal},
		"an OUTCOME 409 keeps its writes":        {http.StatusConflict, replayOutcome, storeAndKeep},
		"a RETRYABLE 409 releases the key":       {http.StatusConflict, replayRetryable, releaseKey},
		"a RETRYABLE 404 releases the key":       {http.StatusNotFound, replayRetryable, releaseKey},
		"an unclassified 4xx releases the key":   {http.StatusConflict, replayUnset, releaseKey},
		"a 500 is never stored":                  {http.StatusInternalServerError, replayUnset, releaseKey},
		"a 503 is never stored":                  {http.StatusServiceUnavailable, replayUnset, releaseKey},
		"a 5xx marked FINAL is never stored":     {http.StatusInternalServerError, replayFinal, releaseKey},
		"a 5xx marked OUTCOME is never stored":   {http.StatusBadGateway, replayOutcome, releaseKey},
		"a 3xx has no class, so the key is free": {http.StatusFound, replayUnset, releaseKey},
	} {
		rec := &capture{header: http.Header{}, code: c.code, class: c.class}
		if got := rec.verdict(); got != c.want {
			t.Errorf("%s: verdict %d, want %d", name, got, c.want)
		}
	}
}

// spyTx counts what the middleware does to the request's transaction; the savepoint is SQL on
// the same transaction, so the statements tell what happened to the handler's writes.
type spyTx struct {
	fakeTx
	commits, rollbacks int
	failOn             string // an Exec whose SQL starts with this fails (a poisoned transaction)
}

func (s *spyTx) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	if s.failOn != "" && strings.HasPrefix(sql, s.failOn) {
		s.execSQL = append(s.execSQL, sql)
		return pgconn.CommandTag{}, errors.New("current transaction is aborted")
	}
	return s.fakeTx.Exec(ctx, sql, args...)
}

func (s *spyTx) Commit(context.Context) error   { s.commits++; return nil }
func (s *spyTx) Rollback(context.Context) error { s.rollbacks++; return nil }

func (s *spyTx) ran(prefix string) bool {
	return slices.ContainsFunc(s.execSQL, func(q string) bool { return strings.HasPrefix(q, prefix) })
}

func (s *spyTx) stored() bool { return s.ran("INSERT INTO idempotency_keys") }

// runHandler drives runFreshRequest with a handler that answers through the production error
// writer, so the class marking is the one production runs.
func runHandler(tx *spyTx, answer func(w http.ResponseWriter, r *http.Request)) *httptest.ResponseRecorder {
	out := httptest.NewRecorder()
	bind := func(ctx context.Context, _ pgx.Tx) context.Context { return ctx }
	runFreshRequest(context.Background(), out, httptest.NewRequest(http.MethodPost, "/assignments/a/reoptimisations", nil), tx, bind, "k", "fp", http.HandlerFunc(answer))
	return out
}

func refuseWith(err error) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) { writeError(w, r, err) }
}

func outcomeRefusal(w http.ResponseWriter, r *http.Request) {
	w.(replayMarker).MarkReplay(replayOutcome)
	writeProblem(w, r, problem{http.StatusConflict, "recorded-then-refused", "Recorded then refused"}, "the command committed and then refused")
}

func TestTheMiddlewareActsOnTheClassOfTheRefusal(t *testing.T) {
	for name, c := range map[string]struct {
		answer                       func(http.ResponseWriter, *http.Request)
		code                         int
		stored, released, keptWrites bool
	}{
		"FINAL: the writes roll back, the refusal is stored":   {refuseWith(errMalformedRequest), 400, true, false, false},
		"RETRYABLE: everything rolls back, nothing is stored":  {refuseWith(ports.ErrConcurrentUpdate), 409, false, true, false},
		"RETRYABLE 404: everything rolls back, nothing stored": {refuseWith(ports.ErrNotFound), 404, false, true, false},
		"unclassified refusal: everything rolls back": {func(w http.ResponseWriter, r *http.Request) {
			writeProblem(w, r, problem{http.StatusConflict, "x", "X"}, "no class")
		}, 409, false, true, false},
		"5xx: everything rolls back, nothing is stored":      {refuseWith(errors.New("boom")), 500, false, true, false},
		"OUTCOME: the writes commit with the stored refusal": {outcomeRefusal, 409, true, false, true},
		"2xx: the writes commit with the stored answer": {func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, http.StatusOK, map[string]string{"id": "x"})
		}, 200, true, false, true},
		"a refusal over an aborted transaction is still stored": {refuseWith(errMalformedRequest), 400, true, false, false},
	} {
		tx := &spyTx{}
		if strings.Contains(name, "aborted") {
			tx.failOn = "RELEASE SAVEPOINT" // a FINAL refusal never releases; it rolls back TO the savepoint, which heals
		}
		out := runHandler(tx, c.answer)
		if out.Code != c.code {
			t.Errorf("%s: answered %d, want %d", name, out.Code, c.code)
		}
		if tx.stored() != c.stored {
			t.Errorf("%s: answer stored = %v, want %v", name, tx.stored(), c.stored)
		}
		if released := tx.rollbacks == 1 && tx.commits == 0; released != c.released {
			t.Errorf("%s: %d commits %d rollbacks, released = %v, want %v", name, tx.commits, tx.rollbacks, released, c.released)
		}
		if kept := tx.ran("RELEASE SAVEPOINT"); kept != c.keptWrites {
			t.Errorf("%s: savepoint released (writes kept) = %v, want %v", name, kept, c.keptWrites)
		}
		if discarded := tx.ran("ROLLBACK TO SAVEPOINT"); discarded != (c.stored && !c.keptWrites) {
			t.Errorf("%s: savepoint rolled back = %v, want %v", name, discarded, c.stored && !c.keptWrites)
		}
	}
}

func TestEveryEntryIsReleasedOrStoredAsItsClassSays(t *testing.T) {
	for _, e := range problemCatalogue {
		tx := &spyTx{}
		out := runHandler(tx, refuseWith(fmt.Errorf("%w: x", e.err)))
		wantStored := e.replay == replayFinal || e.replay == replayOutcome
		if out.Code != e.p.status || tx.stored() != wantStored {
			t.Errorf("%s (%s): answered %d stored=%v, want %d stored=%v", e.p.slug, e.replay, out.Code, tx.stored(), e.p.status, wantStored)
		}
		if released := tx.rollbacks == 1 && tx.commits == 0; released == wantStored {
			t.Errorf("%s (%s): %d commits %d rollbacks, released=%v", e.p.slug, e.replay, tx.commits, tx.rollbacks, released)
		}
	}
}

// A 2xx or an OUTCOME answer whose savepoint cannot be released means the handler's writes are
// gone (an aborted transaction): storing it would be a lie, so the request fails and the key retries.
func TestAnAnswerWhoseWritesAreGoneIsNeverStored(t *testing.T) {
	for name, answer := range map[string]func(http.ResponseWriter, *http.Request){
		"2xx": func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, http.StatusOK, map[string]string{"id": "x"})
		},
		"OUTCOME": outcomeRefusal,
	} {
		tx := &spyTx{failOn: "RELEASE SAVEPOINT"}
		out := runHandler(tx, answer)
		if out.Code != http.StatusInternalServerError || tx.stored() || tx.commits != 0 || tx.rollbacks != 1 {
			t.Errorf("%s: answered %d stored=%v commits=%d rollbacks=%d, want a 500 with everything rolled back", name, out.Code, tx.stored(), tx.commits, tx.rollbacks)
		}
	}
}

// ---- the records that must say what the code does ----------------------------------------------------

var adrRow = regexp.MustCompile("(?m)^\\| `([a-z0-9-]+)` \\| (FINAL|OUTCOME|RETRYABLE) \\|")

func repoFile(t *testing.T, rel ...string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(append([]string{"..", "..", "..", ".."}, rel...)...))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func catalogueClasses() map[string]string {
	out := map[string]string{}
	for _, e := range problemCatalogue {
		out[e.p.slug] = e.replay.String()
	}
	return out
}

func TestTheADRDeclaresTheClassOfEveryProblem(t *testing.T) {
	matches, err := filepath.Glob(filepath.Join("..", "..", "..", "..", "docs", "adr", "0019-*.md"))
	if err != nil || len(matches) != 1 {
		t.Fatalf("docs/adr/0019-*.md = %v (%v), want exactly one ADR", matches, err)
	}
	raw, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatal(err)
	}
	declared := map[string]string{}
	for _, m := range adrRow.FindAllStringSubmatch(string(raw), -1) {
		if prev, dup := declared[m[1]]; dup {
			t.Errorf("the ADR lists %q twice (%s and %s)", m[1], prev, m[2])
		}
		declared[m[1]] = m[2]
	}
	want := catalogueClasses()
	for slug, class := range want {
		if declared[slug] != class {
			t.Errorf("ADR 0019 says %q is %q, the catalogue says %s", slug, declared[slug], class)
		}
	}
	for slug := range declared {
		if _, ok := want[slug]; !ok {
			t.Errorf("ADR 0019 classifies %q, which is not in the catalogue", slug)
		}
	}
}

var retryableSentence = regexp.MustCompile(`release the key \(RETRYABLE\): ([a-z0-9, -]+)\.`)

func TestTheSpecListsTheRetryableProblemTypes(t *testing.T) {
	spec := strings.Join(strings.Fields(repoFile(t, "apis", "openapi.yaml")), " ")
	m := retryableSentence.FindStringSubmatch(spec)
	if m == nil {
		t.Fatal("apis/openapi.yaml does not name the problem types that release the key: \"... release the key (RETRYABLE): a, b, c.\"")
	}
	var want []string
	for slug, class := range catalogueClasses() {
		if class == "RETRYABLE" {
			want = append(want, slug)
		}
	}
	sort.Strings(want)
	got := strings.Split(m[1], ", ")
	sort.Strings(got)
	if !slices.Equal(got, want) {
		t.Errorf("the spec lists %v as RETRYABLE, the catalogue declares %v", got, want)
	}
	if strings.Contains(spec, "every stored 2xx and 4xx outcome is replayed") || strings.Contains(spec, "replays the first response for 24 hours;") {
		t.Error("the spec still says every repeat replays the first response: a RETRYABLE refusal is not replayed")
	}
	if !strings.Contains(spec, "when that response is final") {
		t.Error("the spec does not say that only a final response is replayed")
	}
}
