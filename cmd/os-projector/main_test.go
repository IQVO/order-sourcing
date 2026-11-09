package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"
	"time"
)

func envOf(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestLoadConfigDefaults(t *testing.T) {
	c, err := loadConfig(envOf(map[string]string{"ANALYTICS_DATABASE_URL": "postgres://a", "KAFKA_BROKERS": " k1:9092, ,k2:9092 "}))
	if err != nil {
		t.Fatal(err)
	}
	want := config{analyticsURL: "postgres://a", brokers: []string{"k1:9092", "k2:9092"}, group: "order-sourcing-analytics", adminAddr: ":8091"}
	if !reflect.DeepEqual(c, want) {
		t.Fatalf("config = %+v, want %+v", c, want)
	}
}

func TestLoadConfigOverrides(t *testing.T) {
	c, err := loadConfig(envOf(map[string]string{"ANALYTICS_DATABASE_URL": "postgres://a", "KAFKA_BROKERS": "k:1",
		"ANALYTICS_CONSUMER_GROUP": "g", "ADMIN_ADDR": ":9"}))
	if err != nil || c.group != "g" || c.adminAddr != ":9" {
		t.Fatalf("config = %+v, %v", c, err)
	}
}

func TestLoadConfigRefusals(t *testing.T) {
	if _, err := loadConfig(envOf(map[string]string{"KAFKA_BROKERS": "k:1"})); !errors.Is(err, errMissingAnalyticsURL) {
		t.Fatalf("no DSN: %v", err)
	}
	if _, err := loadConfig(envOf(map[string]string{"ANALYTICS_DATABASE_URL": "postgres://a", "KAFKA_BROKERS": " , "})); !errors.Is(err, errMissingBrokers) {
		t.Fatalf("no brokers: %v", err)
	}
	if err := run(context.Background(), envOf(map[string]string{})); !errors.Is(err, errMissingAnalyticsURL) {
		t.Fatalf("run must refuse before opening anything: %v", err)
	}
}

type fakeDB struct{ err error }

func (f fakeDB) Ping(context.Context) error { return f.err }

func get(t *testing.T, h http.Handler, path string) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), http.MethodGet, path, nil))
	return rec.Code, rec.Body.String()
}

func TestAdminMux(t *testing.T) {
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	var notReady atomic.Bool
	h := newAdminMux(&notReady, fakeDB{}, quiet, "svc")
	if code, body := get(t, h, "/healthz"); code != http.StatusOK || body != `{"status":"ok"}` {
		t.Fatalf("healthz = %d %s", code, body)
	}
	if code, body := get(t, h, "/readyz"); code != http.StatusOK || body != `{"status":"ready"}` {
		t.Fatalf("readyz = %d %s", code, body)
	}
	if code, _ := get(t, newAdminMux(&notReady, fakeDB{err: errors.New("down")}, quiet, "svc"), "/readyz"); code != http.StatusServiceUnavailable {
		t.Fatalf("readyz with the database down = %d", code)
	}
	notReady.Store(true)
	if code, body := get(t, h, "/readyz"); code != http.StatusServiceUnavailable || body != `{"status":"not_ready"}` {
		t.Fatalf("draining readyz = %d %s", code, body)
	}
	if code, _ := get(t, h, "/healthz"); code != http.StatusOK {
		t.Fatal("liveness must not flip on shutdown")
	}
}

func TestDrainConsumer(t *testing.T) {
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	adminErr := errors.New("admin")
	boom := errors.New("reader failed")
	cases := []struct {
		name     string
		consumer error
		admin    error
		want     []error
		notWant  error
	}{
		{"graceful", context.Canceled, nil, nil, context.Canceled},
		{"a clean return", nil, nil, nil, nil},
		{"a real failure surfaces", boom, nil, []error{boom}, nil},
		{"joined with the admin error", boom, adminErr, []error{boom, adminErr}, nil},
		{"cancellation keeps the admin error", context.Canceled, adminErr, []error{adminErr}, context.Canceled},
	}
	for _, c := range cases {
		done := make(chan error, 1)
		done <- c.consumer
		err := drainConsumer(context.Background(), done, c.admin, quiet)
		for _, w := range c.want {
			if !errors.Is(err, w) {
				t.Errorf("%s: err = %v, want %v in it", c.name, err, w)
			}
		}
		if len(c.want) == 0 && err != nil {
			t.Errorf("%s: err = %v, want nil", c.name, err)
		}
		if c.notWant != nil && errors.Is(err, c.notWant) {
			t.Errorf("%s: err = %v must not carry %v", c.name, err, c.notWant)
		}
	}
}

func TestDrainConsumerDeadline(t *testing.T) {
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	adminErr := errors.New("admin")
	if err := drainConsumer(ctx, make(chan error), adminErr, quiet); !errors.Is(err, adminErr) {
		t.Fatalf("a consumer that misses the deadline leaves the admin error, got %v", err)
	}
}

func TestNewLoggerLevels(t *testing.T) {
	cases := map[string]slog.Level{"debug": slog.LevelDebug, "WARN": slog.LevelWarn, "warning": slog.LevelWarn,
		"error": slog.LevelError, "": slog.LevelInfo, "verbose": slog.LevelInfo}
	for in, want := range cases {
		l := newLogger(in)
		if !l.Enabled(context.Background(), want) || (want > slog.LevelDebug && l.Enabled(context.Background(), want-1)) {
			t.Errorf("newLogger(%q) does not log from %v exactly", in, want)
		}
	}
}
