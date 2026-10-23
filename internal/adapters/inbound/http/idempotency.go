package http

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// IdempotencyKeyHeader is the request header of an idempotent route.
const IdempotencyKeyHeader = "Idempotency-Key"

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
// (route, key); the stored outcome (4xx included) is replayed for 24 hours; the
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
			if len(key) > maxKeyLen {
				writeProblem(w, r, idempotencyKeyTooLong, "the Idempotency-Key is longer than 128 characters")
				return
			}
			serve(w, r, pool, bind, key, next)
		})
	}
}

func serve(w http.ResponseWriter, r *http.Request, pool *pgxpool.Pool, bind TxBinder, key string, next http.Handler) {
	ctx := r.Context()
	route := r.Method + " " + r.URL.Path
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
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, route+"\x1f"+key); err != nil {
		_ = tx.Rollback(context.WithoutCancel(ctx))
		writeError(w, r, err)
		return
	}
	if replayed, err := replay(ctx, tx, w, route, key); err != nil || replayed {
		_ = tx.Rollback(context.WithoutCancel(ctx))
		if err != nil {
			writeError(w, r, err)
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
	if err := store(ctx, tx, route, key, rec); err != nil {
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

func replay(ctx context.Context, tx pgx.Tx, w http.ResponseWriter, route, key string) (bool, error) {
	var (
		status int
		body   []byte
		raw    []byte
	)
	err := tx.QueryRow(ctx, `SELECT status, body, headers FROM idempotency_keys WHERE route = $1 AND key = $2 AND created_at > now() - $3::interval`,
		route, key, idempotencyTTL.String()).Scan(&status, &body, &raw)
	if err == pgx.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var stored http.Header
	if len(raw) > 0 && json.Unmarshal(raw, &stored) == nil {
		for k, vs := range stored {
			for _, v := range vs {
				w.Header().Add(k, v)
			}
		}
	}
	w.Header().Set("Idempotent-Replayed", "true")
	w.WriteHeader(status)
	_, _ = w.Write(body)
	return true, nil
}

func store(ctx context.Context, tx pgx.Tx, route, key string, rec *capture) error {
	headers, err := json.Marshal(rec.header)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO idempotency_keys (route, key, status, body, headers) VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (route, key) DO UPDATE SET status = $3, body = $4, headers = $5, created_at = now()`,
		route, key, rec.code, append([]byte{}, rec.body.Bytes()...), headers)
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
