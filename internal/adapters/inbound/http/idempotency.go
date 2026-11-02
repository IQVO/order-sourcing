package http

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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
// key; the stored outcome (4xx included) is replayed for 24 hours; the
// handler's writes, the outbox rows and the stored outcome commit together. A
// 5xx or a panic rolls everything back and is never stored, so the key can retry.
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
	rec := &capture{header: http.Header{}}
	next.ServeHTTP(rec, r.WithContext(bind(ctx, tx)))
	if rec.code == 0 {
		rec.code = http.StatusOK
	}
	if rec.code >= http.StatusInternalServerError {
		_ = tx.Rollback(context.WithoutCancel(ctx))
		flush(w, rec)
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
	if err == pgx.ErrNoRows {
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
}

func (c *capture) Header() http.Header { return c.header }

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
