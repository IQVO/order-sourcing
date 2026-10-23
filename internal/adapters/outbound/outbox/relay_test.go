package outbox_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	relay "github.com/claudioed/order-sourcing/internal/adapters/outbound/outbox"
	"github.com/claudioed/order-sourcing/internal/application/outbox"
)

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// store is an in-memory Drain with the Postgres adapter's semantics: id order, stop at the first failure,
// rows at the attempt cap are not claimed.
type store struct {
	mu   sync.Mutex
	rows []*row
}

type row struct {
	msg      outbox.Message
	attempts int
	done     bool
}

func (s *store) add(types ...string) {
	for _, ty := range types {
		s.rows = append(s.rows, &row{msg: outbox.Message{EventID: ty, EventType: ty, Topic: "t", Key: []byte("k")}})
	}
}

func (s *store) Drain(ctx context.Context, limit, maxAttempts int, send func(context.Context, outbox.Message) error) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, r := range s.rows {
		if r.done || (maxAttempts > 0 && r.attempts >= maxAttempts) {
			continue
		}
		if n == limit {
			break
		}
		r.attempts++
		if err := send(ctx, r.msg); err != nil {
			return n, &outbox.SendFailure{RowID: 1, Message: r.msg, Attempts: r.attempts, Err: err}
		}
		r.done = true
		n++
	}
	return n, nil
}

type sink struct {
	sent   []string
	failOn string
}

func (k *sink) Send(_ context.Context, msgs ...outbox.Message) error {
	for _, m := range msgs {
		if m.EventType == k.failOn {
			return errors.New("broker down")
		}
		k.sent = append(k.sent, m.EventType)
	}
	return nil
}

func TestRelayOncePublishesInOrderAndOnlyOnce(t *testing.T) {
	st, sk := &store{}, &sink{}
	st.add("a", "b", "c")
	r := relay.NewRelay(st, sk, quiet())
	if n, err := r.RelayOnce(context.Background()); n != 3 || err != nil {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if n, _ := r.RelayOnce(context.Background()); n != 0 || len(sk.sent) != 3 || sk.sent[0] != "a" || sk.sent[2] != "c" {
		t.Fatalf("a published row is never sent again: %v", sk.sent)
	}
}

func TestRelayStopsAtTheFirstFailureAndKeepsOrder(t *testing.T) {
	st, sk := &store{}, &sink{failOn: "b"}
	st.add("a", "b", "c")
	r := relay.NewRelay(st, sk, quiet())
	n, err := r.RelayOnce(context.Background())
	var sf *outbox.SendFailure
	if n != 1 || !errors.As(err, &sf) || sf.Message.EventType != "b" {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if len(sk.sent) != 1 {
		t.Fatalf("c must not overtake the failed b: %v", sk.sent)
	}
	sk.failOn = ""
	if n, err := r.RelayOnce(context.Background()); n != 2 || err != nil || sk.sent[1] != "b" || sk.sent[2] != "c" {
		t.Fatalf("the retry sends b then c: n=%d err=%v %v", n, err, sk.sent)
	}
}

func TestAttemptCapParksAPoisonRowAndLogsIt(t *testing.T) {
	st, sk := &store{}, &sink{failOn: "bad"}
	st.add("bad", "ok")
	var logged []string
	h := slog.New(slog.NewTextHandler(writerFunc(func(p []byte) { logged = append(logged, string(p)) }), nil))
	r := relay.NewRelay(st, sk, h, relay.WithMaxAttempts(2))
	for range 2 {
		_, _ = r.RelayOnce(context.Background())
	}
	if len(logged) != 1 {
		t.Fatalf("the cap is announced once, on the attempt that reaches it: %q", logged)
	}
	if n, err := r.RelayOnce(context.Background()); n != 1 || err != nil || sk.sent[0] != "ok" {
		t.Fatalf("a parked row no longer blocks later rows: n=%d err=%v %v", n, err, sk.sent)
	}
}

type writerFunc func([]byte)

func (f writerFunc) Write(p []byte) (int, error) { f(p); return len(p), nil }

func TestOptionsIgnoreNonsense(t *testing.T) {
	st, sk := &store{}, &sink{}
	st.add("a", "b", "c")
	r := relay.NewRelay(st, sk, nil, relay.WithBatchSize(2), relay.WithBatchSize(-1), relay.WithInterval(0), relay.WithMaxAttempts(-5))
	if n, _ := r.RelayOnce(context.Background()); n != 2 {
		t.Fatalf("batch size 2 wins over the ignored -1: %d", n)
	}
}

func TestRunDrainsUntilCancelled(t *testing.T) {
	st, sk := &store{}, &sink{}
	st.add("a", "b", "c")
	ctx, cancel := context.WithCancel(context.Background())
	r := relay.NewRelay(st, sk, quiet(), relay.WithBatchSize(2), relay.WithInterval(time.Millisecond))
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()
	deadline := time.After(2 * time.Second)
	for {
		st.mu.Lock()
		all := st.rows[0].done && st.rows[1].done && st.rows[2].done
		st.mu.Unlock()
		if all {
			break
		}
		select {
		case <-deadline:
			t.Fatal("rows were not drained")
		case <-time.After(time.Millisecond):
		}
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run = %v", err)
	}
}

func TestLogSinkAcceptsEverything(t *testing.T) {
	if err := (relay.LogSink{}).Send(context.Background(), outbox.Message{EventType: "x"}); err != nil {
		t.Fatal(err)
	}
}

func TestRunStopsAtOnceWhenAlreadyCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := relay.NewRelay(&store{}, &sink{}, quiet()).Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run = %v", err)
	}
}
