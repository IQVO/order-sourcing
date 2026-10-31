package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2aclient"

	inbounda2a "github.com/claudioed/order-sourcing/internal/adapters/inbound/a2a"
)

const testAgentURL = "http://order-sourcing-a2a.warehouse-systems.svc.cluster.local:8093/"

// testCard is the real Agent Card, as run() builds it.
func testCard() *a2a.AgentCard {
	return inbounda2a.Card(inbounda2a.Config{AgentURL: testAgentURL, Version: "test"})
}

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// newTestServer serves the real A2A executor (no skill that reaches a database
// is invoked here: the zero Queries is enough for the transport tests) behind
// the real router.
func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(newRouter(inbounda2a.Handler(inbounda2a.NewExecutor(inbounda2a.Deps{})), testCard(), quietLogger(), "order-sourcing-a2a"))
	t.Cleanup(srv.Close)
	return srv
}

func TestRouter_HealthzIsOpenAndServesOK(t *testing.T) {
	srv := newTestServer(t)
	resp, err := http.Get(srv.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || string(body) != `{"status":"ok"}` {
		t.Fatalf("GET /healthz = %d %q, want 200 {\"status\":\"ok\"}", resp.StatusCode, body)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", ct)
	}
}

func TestRouter_ServesTheAgentCardAtTheWellKnownPath(t *testing.T) {
	srv := newTestServer(t)
	resp, err := http.Get(srv.URL + "/.well-known/agent-card.json")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET agent card = %d, want 200", resp.StatusCode)
	}
	for _, want := range []string{`"name":"order-sourcing"`, testAgentURL, `"find_assignments"`, `"assignment_detail"`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("agent card lacks %s: %s", want, raw)
		}
	}
}

// Both mount points answer JSON-RPC, with no credentials (the fleet's
// <CTX>_A2A_ENDPOINT convention is "/a2a", the canonical endpoint is "/"), and
// a request carrying a bearer token behaves identically: nothing reads it.
func TestRouter_BothMountsReachTheJSONRPCHandlerWithoutCredentials(t *testing.T) {
	srv := newTestServer(t)
	// An unknown skill is answered in-band before any use case runs, so a
	// JSON-RPC result proves the request reached the SDK handler at this mount.
	body := `{"jsonrpc":"2.0","id":1,"method":"SendMessage","params":{"message":{"messageId":"m1","role":"ROLE_USER","parts":[{"data":{"skill":"launch_rocket","arguments":{}}}]}}}`
	for _, path := range []string{"/", "/a2a"} {
		for _, auth := range []string{"", "Bearer nonsense"} {
			t.Run(path+" auth="+auth, func(t *testing.T) {
				req, _ := http.NewRequest(http.MethodPost, srv.URL+path, strings.NewReader(body))
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("A2A-Version", "1.0")
				if auth != "" {
					req.Header.Set("Authorization", auth)
				}
				resp, err := http.DefaultClient.Do(req)
				if err != nil {
					t.Fatal(err)
				}
				defer resp.Body.Close()
				raw, _ := io.ReadAll(resp.Body)
				if resp.StatusCode != http.StatusOK || !strings.Contains(string(raw), "unknown-skill") {
					t.Fatalf("POST %s = %d %q, want 200 with the in-band unknown-skill problem", path, resp.StatusCode, raw)
				}
			})
		}
	}
}

// A real A2A client resolves the card served by the binary's own router and
// reaches the agent through it.
func TestRouter_AClientDiscoversTheCardAndTalksToTheAgent(t *testing.T) {
	srv := newTestServer(t)
	card := inbounda2a.Card(inbounda2a.Config{AgentURL: srv.URL + "/a2a", Version: "test"})
	client, err := a2aclient.NewFromCard(context.Background(), card)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.SendMessage(context.Background(), &a2a.SendMessageRequest{
		Message: a2a.NewMessage(a2a.MessageRoleUser, a2a.NewDataPart(map[string]any{"skill": "launch_rocket"})),
	})
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if msg, ok := resp.(*a2a.Message); !ok || len(msg.Parts) != 2 {
		t.Fatalf("reply = %#v, want a two-part message", resp)
	}
}

// A missing DATABASE_URL is a boot error, never a silent in-memory fallback.
func TestBuildDeps_MissingDatabaseFailsBoot(t *testing.T) {
	_, _, err := buildDeps(context.Background(), quietLogger(), "", "")
	if err == nil || !strings.Contains(err.Error(), "DATABASE_URL") {
		t.Fatalf("buildDeps without a database = %v, want an error naming DATABASE_URL", err)
	}
}

// An unreachable database is a boot error after the bounded retries.
func TestBuildDeps_BadDatabaseFailsBoot(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // a cancelled context makes bootretry give up at once
	if _, _, err := buildDeps(ctx, quietLogger(), "postgres://u@127.0.0.1:1/db", "postgres://u@127.0.0.1:1/db"); err == nil {
		t.Fatal("an unreachable database must fail boot")
	}
}

func TestGetenv(t *testing.T) {
	t.Setenv("OS_A2A_TEST_SET", "v")
	t.Setenv("OS_A2A_TEST_EMPTY", "")
	if got := getenv("OS_A2A_TEST_SET", "d"); got != "v" {
		t.Errorf("set: %q", got)
	}
	if got := getenv("OS_A2A_TEST_EMPTY", "d"); got != "d" {
		t.Errorf("empty: %q", got)
	}
}

func TestNewLoggerLevels(t *testing.T) {
	for level, want := range map[string]slog.Level{"debug": slog.LevelDebug, "WARN": slog.LevelWarn, "warning": slog.LevelWarn, "error": slog.LevelError, "": slog.LevelInfo, "bogus": slog.LevelInfo} {
		if got := newLogger(level); !got.Handler().Enabled(context.Background(), want) || (want > slog.LevelDebug && got.Handler().Enabled(context.Background(), want-1)) {
			t.Errorf("newLogger(%q): level %v not the minimum enabled", level, want)
		}
	}
}
