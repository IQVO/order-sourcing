//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/claudioed/order-sourcing/internal/adapters/outbound/kafka"
	"github.com/claudioed/order-sourcing/internal/adapters/outbound/postgres"
	"github.com/claudioed/order-sourcing/internal/application/outbox"
	"github.com/claudioed/order-sourcing/internal/domain/assignment"
	"github.com/claudioed/order-sourcing/internal/domain/shared"
	"github.com/claudioed/order-sourcing/internal/testing/pgtest"
)

func appendEvents(t *testing.T, ob *postgres.Outbox, names ...string) {
	t.Helper()
	at := time.Date(2026, 10, 22, 14, 0, 0, 0, time.UTC)
	for i, n := range names {
		var ev shared.Event
		b := assignment.Base{Header: shared.Header{Version: int64(i + 1), At: at}, AssignmentID: "asg-1"}
		switch n {
		case "cancelled":
			ev = assignment.FulfillmentCancelled{Base: b, OrderID: "ord-1", GroupNo: 1, Reason: "CUSTOMER_REQUEST"}
		default:
			ev = assignment.FulfillmentDelivered{Base: b, OrderID: "ord-1", GroupNo: 1, DeliveredAt: at}
		}
		if err := ob.Append(context.Background(), ev); err != nil {
			t.Fatal(err)
		}
	}
}

func TestOutboxDrainSendsInOrderOncePerRowAndRoutesStreams(t *testing.T) {
	pool := pgtest.NewPool(t)
	ob := postgres.NewOutbox(pool, kafka.Encoder{})
	appendEvents(t, ob, "cancelled", "delivered")
	var topics []string
	n, err := ob.Drain(context.Background(), 10, 0, func(_ context.Context, m outbox.Message) error {
		topics = append(topics, m.Topic)
		return nil
	})
	if n != 2 || err != nil || topics[0] != kafka.TopicEvents || topics[1] != kafka.TopicAnalytics {
		t.Fatalf("n=%d err=%v topics=%v", n, err, topics)
	}
	if n, _ := ob.Drain(context.Background(), 10, 0, func(context.Context, outbox.Message) error { return errors.New("must not be called") }); n != 0 {
		t.Fatal("published rows are not claimed again")
	}
}

func TestOutboxDrainRecordsTheFailureAndParksAtTheCap(t *testing.T) {
	pool := pgtest.NewPool(t)
	ob := postgres.NewOutbox(pool, kafka.Encoder{})
	appendEvents(t, ob, "cancelled", "delivered")
	boom := errors.New("broker down")
	fail := func(context.Context, outbox.Message) error { return boom }

	var sf *outbox.SendFailure
	n, err := ob.Drain(context.Background(), 10, 2, fail)
	if n != 0 || !errors.As(err, &sf) || sf.Attempts != 1 || !errors.Is(err, boom) {
		t.Fatalf("first failure: n=%d err=%v", n, err)
	}
	if _, err := ob.Drain(context.Background(), 10, 2, fail); !errors.As(err, &sf) || sf.Attempts != 2 {
		t.Fatalf("second failure reaches the cap: %v", err)
	}
	var sent []string
	n, err = ob.Drain(context.Background(), 10, 2, func(_ context.Context, m outbox.Message) error { sent = append(sent, m.EventType); return nil })
	if n != 1 || err != nil || len(sent) != 1 {
		t.Fatalf("the parked row no longer blocks the next one: n=%d err=%v sent=%v", n, err, sent)
	}
	var lastErr string
	if err := pool.QueryRow(context.Background(), `SELECT last_error FROM outbox_events WHERE attempts = 2 AND published_at IS NULL`).Scan(&lastErr); err != nil || lastErr != "broker down" {
		t.Fatalf("the parked row keeps its error for inspection: %q %v", lastErr, err)
	}
}

func TestOutboxAppendRefusesAnUnencodableEvent(t *testing.T) {
	pool := pgtest.NewPool(t)
	ob := postgres.NewOutbox(pool, kafka.Encoder{})
	if err := ob.Append(context.Background(), foreign{}); err == nil {
		t.Fatal("an event with no wire form must be refused")
	}
	if err := ob.Append(context.Background()); err != nil {
		t.Fatal("an empty append is a no-op")
	}
}

type foreign struct{ assignment.Base }

func (foreign) EventName() string { return "Nope" }
