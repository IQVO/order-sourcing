package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	outboxrelay "github.com/claudioed/order-sourcing/internal/adapters/outbound/outbox"
)

func envOf(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

var groups = []string{"A_CONSUMER_GROUP", "B_CONSUMER_GROUP"}

func TestLoadRuntimeNamesWhatIsMissing(t *testing.T) {
	ok := map[string]string{"DATABASE_URL": "postgres://x", "KAFKA_BROKERS": "a:9092, b:9092", "A_CONSUMER_GROUP": "ga", "B_CONSUMER_GROUP": "gb"}
	rt, err := loadRuntime(envOf(ok), groups)
	if err != nil || len(rt.brokers) != 2 || rt.brokers[1] != "b:9092" || rt.groups["B_CONSUMER_GROUP"] != "gb" {
		t.Fatalf("rt=%+v err=%v", rt, err)
	}
	cases := map[string]struct {
		drop string
		set  map[string]string
		want string
	}{
		"database":  {"DATABASE_URL", nil, "DATABASE_URL"},
		"brokers":   {"KAFKA_BROKERS", nil, "KAFKA_BROKERS"},
		"group":     {"B_CONSUMER_GROUP", nil, "B_CONSUMER_GROUP"},
		"log mode":  {"", map[string]string{"EVENT_PUBLISHER": "log"}, "EVENT_PUBLISHER=log"},
		"bad mode":  {"", map[string]string{"EVENT_PUBLISHER": "smoke"}, "unknown value"},
		"blank bro": {"", map[string]string{"KAFKA_BROKERS": " , "}, "KAFKA_BROKERS"},
	}
	for name, c := range cases {
		env := map[string]string{}
		for k, v := range ok {
			env[k] = v
		}
		delete(env, c.drop)
		for k, v := range c.set {
			env[k] = v
		}
		if _, err := loadRuntime(envOf(env), groups); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want it to name %q", name, err, c.want)
		}
	}
	env := map[string]string{"DATABASE_URL": "x", "KAFKA_BROKERS": "k", "EVENT_PUBLISHER": "kafka", "A_CONSUMER_GROUP": "a", "B_CONSUMER_GROUP": "b"}
	if _, err := loadRuntime(envOf(env), groups); err != nil {
		t.Fatalf("EVENT_PUBLISHER=kafka is accepted: %v", err)
	}
}

func TestEveryConsumedTopicNeedsAGroup(t *testing.T) {
	got := consumerGroupEnvs(handlersForTest())
	if len(got) != 9 {
		t.Fatalf("groups = %v", got)
	}
	seen := map[string]bool{}
	for _, g := range got {
		if !strings.HasSuffix(g, "_CONSUMER_GROUP") || seen[g] {
			t.Errorf("bad or duplicate group variable %q", g)
		}
		seen[g] = true
	}
}

func logger() (*slog.Logger, *bytes.Buffer) {
	var b bytes.Buffer
	return slog.New(slog.NewTextHandler(&b, nil)), &b
}

func TestRelayIntervalAndAttemptsFallBackWithAWarning(t *testing.T) {
	l, buf := logger()
	for raw, want := range map[string]time.Duration{"": outboxrelay.DefaultInterval, "250ms": 250 * time.Millisecond, "2": 2 * time.Second, "junk": outboxrelay.DefaultInterval, "-1s": outboxrelay.DefaultInterval} {
		if got := parseRelayInterval(raw, l); got != want {
			t.Errorf("interval %q = %v, want %v", raw, got, want)
		}
	}
	for raw, want := range map[string]int{"": outboxrelay.DefaultMaxAttempts, "5": 5, "0": 0, "-1": outboxrelay.DefaultMaxAttempts, "x": outboxrelay.DefaultMaxAttempts} {
		if got := outboxMaxAttempts(raw, l); got != want {
			t.Errorf("attempts %q = %v, want %v", raw, got, want)
		}
	}
	if !strings.Contains(buf.String(), "OUTBOX_RELAY_INTERVAL") || !strings.Contains(buf.String(), "OUTBOX_MAX_ATTEMPTS") {
		t.Fatal("a rejected value is announced")
	}
}

func TestSweepIntervalAndDrainDelay(t *testing.T) {
	l, _ := logger()
	for raw, want := range map[string]time.Duration{"": defaultSweepInterval, "2s": 2 * time.Second, "0s": defaultSweepInterval, "x": defaultSweepInterval} {
		if got := parseSweepInterval(raw, l); got != want {
			t.Errorf("sweep %q = %v, want %v", raw, got, want)
		}
	}
	for raw, want := range map[string]time.Duration{"": defaultShutdownDrain, "0s": 0, "1s": time.Second, "-2s": defaultShutdownDrain, "x": defaultShutdownDrain} {
		t.Setenv("SHUTDOWN_DRAIN_DELAY", raw)
		if got := shutdownDrainDelay(l); got != want {
			t.Errorf("drain %q = %v, want %v", raw, got, want)
		}
	}
}

func TestLoggerLevelsAndGetenv(t *testing.T) {
	for level, want := range map[string]slog.Level{"debug": slog.LevelDebug, "WARN": slog.LevelWarn, "warning": slog.LevelWarn, "error": slog.LevelError, "": slog.LevelInfo, "x": slog.LevelInfo} {
		if got := newLogger(level); !got.Enabled(context.Background(), want) || (want > slog.LevelDebug && got.Enabled(context.Background(), want-1)) {
			t.Errorf("level %q is not %v", level, want)
		}
	}
	t.Setenv("OS_TEST_VAR", "")
	if getenv("OS_TEST_VAR", "fb") != "fb" {
		t.Fatal("fallback")
	}
	t.Setenv("OS_TEST_VAR", "v")
	if getenv("OS_TEST_VAR", "fb") != "v" {
		t.Fatal("value")
	}
}

func TestWorkersAreWatchedAndAwaited(t *testing.T) {
	l, buf := logger()
	a, b := &worker{name: "a", done: make(chan struct{})}, &worker{name: "b", done: make(chan struct{})}
	died := watchWorkers(a, nil, b)
	close(b.done)
	if name := <-died; name != "b" {
		t.Fatalf("died = %s", name)
	}
	await(l, nil)
	await(l, b)
	if buf.Len() != 0 {
		t.Fatal("a stopped worker is not reported")
	}
	if _, _, err := startOutboxRelay(nil, nil, l); !errors.Is(err, errMissingBrokers) {
		t.Fatalf("a relay without brokers refuses to start: %v", err)
	}
}

func TestThePurgeRunsBatchesUntilOneComesBackShort(t *testing.T) {
	var limits []int
	left := 1100
	purge := func(_ context.Context, limit int) (int, error) {
		limits = append(limits, limit)
		n := min(left, limit)
		left -= n
		return n, nil
	}
	if got := purgeOnce(context.Background(), purge, 500, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))); got != 1100 || len(limits) != 3 {
		t.Fatalf("purged %d rows in batches %v, want 1100 in 3 batches of at most 500", got, limits)
	}
}

func TestAFailedPurgeIsLoggedAndEndsThePass(t *testing.T) {
	var logs bytes.Buffer
	calls := 0
	purge := func(context.Context, int) (int, error) {
		calls++
		if calls == 1 {
			return 2, nil
		}
		return 0, errors.New("db down")
	}
	got := purgeOnce(context.Background(), purge, 2, slog.New(slog.NewTextHandler(&logs, nil)))
	if got != 2 || calls != 2 || !strings.Contains(logs.String(), "db down") {
		t.Fatalf("purged %d in %d calls, log %q: want 2 rows, the failure logged and the pass ended", got, calls, logs.String())
	}
}

func TestACancelledPurgeStopsWithoutLoggingAFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var logs bytes.Buffer
	purge := func(context.Context, int) (int, error) { cancel(); return 0, context.Canceled }
	purgeOnce(ctx, purge, 10, slog.New(slog.NewTextHandler(&logs, nil)))
	if strings.Contains(logs.String(), "failed") {
		t.Fatalf("a shutdown is not a failure: %q", logs.String())
	}
}

func TestThePurgeWorkerStopsWhenItsStopIsCalled(t *testing.T) {
	db := noopExec{}
	w, stop := startIdempotencyPurge(db, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	stop()
	select {
	case <-w.done:
	case <-time.After(5 * time.Second):
		t.Fatal("the purge worker did not stop")
	}
}

type noopExec struct{}

func (noopExec) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.NewCommandTag("DELETE 0"), nil
}
