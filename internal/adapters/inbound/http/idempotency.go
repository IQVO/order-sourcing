package http

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// IdempotencyKeyHeader is the request header of an idempotent route;
// IdempotencyReplayedHeader marks (only) a replayed answer.
const (
	IdempotencyKeyHeader      = "Idempotency-Key"
	IdempotencyReplayedHeader = "Idempotency-Replayed"
)

const (
	maxKeyLen      = 128
	idempotencyTTL = 24 * time.Hour
)

// TxBinder returns a child context carrying tx, so the use case's own unit of
// work JOINS the transaction the middleware opened. cmd wires the outbound
// adapter's binder in; adapters never import each other.
type TxBinder func(ctx context.Context, tx pgx.Tx) context.Context

// RequireIdempotencyKey is the transactional Idempotency-Key middleware of a
// route. A transaction-scoped advisory lock serialises two requests with the same
// key. What happens to the answer is its replay class (ADR 0019), declared on
// the catalogue entry of its problem type and marked on the capture by
// writeError: a 2xx is stored with the handler's writes, the outbox rows and the
// key row in one commit; a FINAL refusal is stored and replayed for 24 hours (its
// writes roll back); an OUTCOME refusal commits the handler's writes with the
// stored problem; a RETRYABLE refusal, a refusal with no class, a 5xx and a panic
// roll everything back, key row included, and are never stored, so the same key
// runs the real work again. A request waiting on the key meanwhile finds no row
// and runs the handler itself. A different body under a released key is a new
// request, not idempotency-key-reused.
func RequireIdempotencyKey(pool *pgxpool.Pool, bind TxBinder) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key := r.Header.Get(IdempotencyKeyHeader)
			if key == "" {
				writeProblem(w, r, idempotencyKeyRequired, "send an Idempotency-Key header with a unique value per logical request")
				return
			}
			if !validKey(key) {
				writeProblem(w, r, idempotencyKeyInvalid, "the Idempotency-Key must be 1 to 128 printable ASCII characters")
				return
			}
			body, err := io.ReadAll(r.Body)
			if err != nil {
				writeError(w, r, errMalformedRequest)
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
			sum := sha256.Sum256(body)
			serve(w, r, pool, bind, key, r.Method+" "+r.URL.Path+" "+hex.EncodeToString(sum[:]), next)
		})
	}
}

// validKey reports whether key is 1 to 128 printable ASCII characters.
func validKey(key string) bool {
	if len(key) == 0 || len(key) > maxKeyLen {
		return false
	}
	for i := 0; i < len(key); i++ {
		if key[i] < 0x20 || key[i] > 0x7e {
			return false
		}
	}
	return true
}

// serve runs a genuinely new key: capture the response, persist the outcome in
// the same transaction, commit, and only then write to the real client.
func serve(w http.ResponseWriter, r *http.Request, pool *pgxpool.Pool, bind TxBinder, key, fingerprint string, next http.Handler) {
	ctx := r.Context()
	tx, err := pool.Begin(ctx)
	if err != nil {
		writeError(w, r, err)
		return
	}
	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback(context.WithoutCancel(ctx))
			panic(p)
		}
	}()
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, key); err != nil {
		_ = tx.Rollback(context.WithoutCancel(ctx))
		writeError(w, r, err)
		return
	}
	replayed, reused, err := replay(ctx, tx, w, key, fingerprint)
	if err != nil || replayed || reused {
		_ = tx.Rollback(context.WithoutCancel(ctx))
		switch {
		case err != nil:
			writeError(w, r, err)
		case reused:
			writeProblem(w, r, idempotencyKeyReused, "this Idempotency-Key was already used with a different request")
		}
		return
	}
	runFreshRequest(ctx, w, r, tx, bind, key, fingerprint, next)
}

// runFreshRequest runs the handler behind a SAVEPOINT of tx and settles the answer by its verdict.
//
// The savepoint exists because a mapped domain conflict (a racing request losing
// on a unique index, say) fails a statement on the server and leaves the
// transaction ABORTED even though the handler answers a truthful 4xx. A FINAL
// refusal is settled with ROLLBACK TO SAVEPOINT, which discards the handler's
// partial writes and heals the transaction, so the refusal is stored and
// committed like any other (otherwise the key write hits SQLSTATE 25P02 and the
// client sees a 500). A 2xx or OUTCOME answer behind a failed RELEASE is a lie
// (its writes are gone): everything rolls back and the request fails, and the
// same key retries the real work.
func runFreshRequest(ctx context.Context, w http.ResponseWriter, r *http.Request, tx pgx.Tx, bind TxBinder, key, fingerprint string, next http.Handler) {
	if _, err := tx.Exec(ctx, "SAVEPOINT handler_run"); err != nil {
		_ = tx.Rollback(context.WithoutCancel(ctx))
		writeError(w, r, err)
		return
	}
	rec := &capture{header: http.Header{}}
	next.ServeHTTP(rec, r.WithContext(bind(ctx, tx)))
	if rec.code == 0 {
		rec.code = http.StatusOK
	}
	v := rec.verdict()
	if v == releaseKey {
		// A 5xx and a RETRYABLE refusal are never cached: roll everything back,
		// the key row included, so the same key can run the real work again.
		_ = tx.Rollback(context.WithoutCancel(ctx))
		flush(w, rec)
		return
	}
	if err := settleHandlerSavepoint(ctx, tx, v); err != nil {
		_ = tx.Rollback(context.WithoutCancel(ctx))
		writeError(w, r, err)
		return
	}
	if err := store(ctx, tx, key, fingerprint, rec); err != nil {
		_ = tx.Rollback(context.WithoutCancel(ctx))
		writeError(w, r, err)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		writeError(w, r, err)
		return
	}
	flush(w, rec)
}

// settleHandlerSavepoint closes the handler's savepoint by the verdict: a 2xx or
// an OUTCOME refusal keeps its writes (RELEASE; a failure means the transaction
// is aborted and the writes are gone, so the caller fails the request), a FINAL
// refusal discards them (ROLLBACK TO heals an aborted transaction).
func settleHandlerSavepoint(ctx context.Context, tx pgx.Tx, v verdict) error {
	if v == storeRefusal {
		return execOnly(ctx, tx, "ROLLBACK TO SAVEPOINT handler_run")
	}
	return execOnly(ctx, tx, "RELEASE SAVEPOINT handler_run")
}

func execOnly(ctx context.Context, tx pgx.Tx, sql string) error {
	_, err := tx.Exec(ctx, sql)
	return err
}

// replay answers a stored outcome for the same key and fingerprint (replayed);
// the same key with another fingerprint is reused (409 idempotency-key-reused).
func replay(ctx context.Context, tx pgx.Tx, w http.ResponseWriter, key, fingerprint string) (replayed, reused bool, err error) {
	var (
		status int
		body   []byte
		raw    []byte
		stored string
	)
	err = tx.QueryRow(ctx, `SELECT fingerprint, status, body, headers FROM idempotency_keys WHERE key = $1 AND created_at > now() - $2::interval`,
		key, idempotencyTTL.String()).Scan(&stored, &status, &body, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, false, nil
	}
	if err != nil {
		return false, false, err
	}
	if stored != fingerprint {
		return false, true, nil
	}
	var hdr http.Header
	if len(raw) > 0 && json.Unmarshal(raw, &hdr) == nil {
		for k, vs := range hdr {
			for _, v := range vs {
				w.Header().Add(k, v)
			}
		}
	}
	w.Header().Set(IdempotencyReplayedHeader, "true")
	w.WriteHeader(status)
	_, _ = w.Write(body)
	return true, false, nil
}

func store(ctx context.Context, tx pgx.Tx, key, fingerprint string, rec *capture) error {
	headers, err := json.Marshal(rec.header)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO idempotency_keys (key, fingerprint, status, body, headers) VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (key) DO UPDATE SET fingerprint = $2, status = $3, body = $4, headers = $5, created_at = now()`,
		key, fingerprint, rec.code, append([]byte{}, rec.body.Bytes()...), headers)
	return err
}

// capture records the handler's response so it can be stored and committed
// BEFORE anything reaches the client.
type capture struct {
	header http.Header
	code   int
	body   bytes.Buffer
	// class is the replay class writeError marked (replayMarker); replayUnset
	// when the handler answered without a catalogue problem.
	class replayClass
}

func (c *capture) Header() http.Header { return c.header }

// MarkReplay implements replayMarker.
func (c *capture) MarkReplay(class replayClass) { c.class = class }

// verdict is the whole of what the middleware decides about an answer: a 2xx is
// stored with its writes, a 5xx is never stored, and every other answer is ruled
// by the replay class its catalogue entry declares. A refusal that declares none
// is not stored either: the key stays free.
func (c *capture) verdict() verdict {
	switch {
	case c.code >= http.StatusOK && c.code < http.StatusMultipleChoices:
		return storeAndKeep
	case c.code >= http.StatusInternalServerError:
		return releaseKey
	}
	switch c.class {
	case replayFinal:
		return storeRefusal
	case replayOutcome:
		return storeAndKeep
	}
	return releaseKey
}

func (c *capture) WriteHeader(code int) {
	if c.code == 0 {
		c.code = code
	}
}

func (c *capture) Write(b []byte) (int, error) {
	if c.code == 0 {
		c.code = http.StatusOK
	}
	return c.body.Write(b)
}

func flush(w http.ResponseWriter, rec *capture) {
	for k, vs := range rec.header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(rec.code)
	_, _ = w.Write(rec.body.Bytes())
}
