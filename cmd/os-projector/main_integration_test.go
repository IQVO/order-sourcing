//go:build integration

package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"testing"
	"time"

	inboundkafka "github.com/claudioed/order-sourcing/internal/adapters/inbound/kafka"
	"github.com/claudioed/order-sourcing/internal/adapters/outbound/analyticsstore"
	outboundkafka "github.com/claudioed/order-sourcing/internal/adapters/outbound/kafka"
	"github.com/claudioed/order-sourcing/internal/domain/assignment"
	"github.com/claudioed/order-sourcing/internal/domain/shared"
	"github.com/claudioed/order-sourcing/internal/testing/kafkatest"
	"github.com/claudioed/order-sourcing/internal/testing/pgtest"
)

func TestMain(m *testing.M) {
	code := m.Run()
	kafkatest.Shutdown()
	pgtest.Shutdown()
	os.Exit(code)
}

func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	return l.Addr().String()
}

func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(90 * time.Second); time.Now().Before(deadline); time.Sleep(200 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

// The real entry point: env -> embedded analytical migrations on an EMPTY
// database -> /readyz -> the consumer projects an event the real encoder wrote
// to the real analytics topic -> a cancelled context drains to a clean exit.
func TestProjectorBootsMigratesProjectsAndStopsCleanly(t *testing.T) {
	url := pgtest.EmptyURL(t)
	brokers := kafkatest.Brokers(t)
	for _, topic := range inboundkafka.OwnTopics() {
		kafkatest.CreateTopic(t, topic) // a group joining a missing topic never gets its partitions
	}
	at := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	cost := int64(250)
	decided := assignment.FulfillmentSourcingDecided{Base: assignment.Base{Header: shared.Header{Version: 2, At: at}, AssignmentID: "asg-projector-1"},
		OrderID: "ord-1", GroupNo: 1, ReleaseNo: 1, NodeID: "n1", NodeRegionID: "r1", DestinationRegionID: "r1", Legs: 1,
		EstimatedCostCents: &cost, Units: 2, CandidateCount: 3, DecidedAt: at}
	msg, err := outboundkafka.Encoder{}.Encode(decided)
	if err != nil {
		t.Fatal(err)
	}
	if msg.Topic != inboundkafka.OwnAnalyticsTopic {
		t.Fatalf("the decision is published on %s", msg.Topic)
	}
	kafkatest.Produce(t, msg.Topic, kafkatest.Message{Key: string(msg.Key), Value: msg.Value})

	addr := freeAddr(t)
	env := map[string]string{"ANALYTICS_DATABASE_URL": url, "KAFKA_BROKERS": brokers[0], "ADMIN_ADDR": addr, "LOG_LEVEL": "error",
		"ANALYTICS_CONSUMER_GROUP": fmt.Sprintf("os-projector-itest-%d", time.Now().UnixNano())}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- run(ctx, func(k string) string { return env[k] }) }()
	t.Cleanup(cancel)

	waitUntil(t, "/readyz 200", func() bool {
		resp, err := http.Get("http://" + addr + "/readyz")
		if err != nil {
			return false
		}
		_ = resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	})
	pool, err := analyticsstore.NewPool(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	waitUntil(t, "the decision projected", func() bool {
		var n int
		err := pool.QueryRow(context.Background(), `SELECT count(*) FROM sourcing_decisions WHERE assignment_id = 'asg-projector-1' AND in_region AND estimated_cost_cents = 250`).Scan(&n)
		return err == nil && n == 1
	})
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("a cancelled projector must drain cleanly, got %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the projector did not stop")
	}
}
