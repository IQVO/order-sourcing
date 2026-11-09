package kafka

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	kafkago "github.com/segmentio/kafka-go"

	"github.com/claudioed/order-sourcing/internal/adapters/kafka/cloudevents"
	"github.com/claudioed/order-sourcing/internal/analytics/report"
)

// fakeProjection records the applied events; errs are returned one per Apply
// call first (nil entries succeed).
type fakeProjection struct {
	mu     sync.Mutex
	events []report.Event
	errs   []error
}

func (f *fakeProjection) Apply(_ context.Context, e report.Event) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.errs) > 0 {
		err := f.errs[0]
		f.errs = f.errs[1:]
		if err != nil {
			return false, err
		}
	}
	f.events = append(f.events, e)
	return true, nil
}

func (f *fakeProjection) applied() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.events)
}

var (
	anEventTime = time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	decidedAt   = time.Date(2026, 11, 3, 11, 30, 0, 0, time.UTC)
	decidedRFC  = "2026-11-03T11:30:00Z"
)

// ownEvent encodes one of this context's events through the shared envelope
// builder (the one the outbox encoder uses).
func ownEvent(t *testing.T, name, stream, subject string, data map[string]any) []byte {
	t.Helper()
	b, err := cloudevents.New(cloudevents.Spec{ID: "evt-" + name, Entity: entityAssignment, EventName: name, Subject: subject,
		Time: anEventTime, Stream: stream, Version: 1, Data: data})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func decisionData() map[string]any {
	return map[string]any{"assignmentId": "asg-1", "orderId": "ord-1", "groupNo": 2, "releaseNo": 1, "nodeId": "n1", "nodeRegionId": "r1",
		"destinationRegionId": "r1", "inRegion": true, "legs": 1, "estimatedCostCents": 450, "costComplete": true, "units": 3,
		"candidateCount": 4, "decidedAt": decidedRFC, "futureField": "ignored"}
}

func analyticsConsumer(p report.Projection, dlq DeadLetterWriter, logger *slog.Logger) *AnalyticsConsumer {
	if logger == nil {
		logger = quietLogger()
	}
	return &AnalyticsConsumer{Projection: p, DLQ: dlq, Topic: OwnEventsTopic, DLQTopic: AnalyticsDLQTopic, Logger: logger}
}

func handleOne(t *testing.T, value []byte) (*fakeProjection, *recordedDLQ) {
	t.Helper()
	p, dlq := &fakeProjection{}, &recordedDLQ{}
	if err := analyticsConsumer(p, dlq, nil).HandleMessage(context.Background(), kafkago.Message{Value: value}); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	return p, dlq
}

func TestAnalyticsConsumerProjectsTheDecision(t *testing.T) {
	p, dlq := handleOne(t, ownEvent(t, "FulfillmentSourcingDecided", cloudevents.StreamAnalytics, "asg-1", decisionData()))
	in, cost := true, int64(450)
	want := report.Event{Kind: report.KindFulfillmentDecided, EventID: "evt-FulfillmentSourcingDecided", At: anEventTime, AssignmentID: "asg-1",
		OrderID: "ord-1", GroupNo: 2, OccurredAt: decidedAt, Decision: report.Decision{ReleaseNo: 1, NodeID: "n1", NodeRegionID: "r1",
			DestinationRegionID: "r1", InRegion: &in, Legs: 1, EstimatedCostCents: &cost, CostComplete: true, Units: 3, CandidateCount: 4}}
	if len(dlq.msgs) != 0 || len(p.events) != 1 || !reflect.DeepEqual(p.events[0], want) {
		t.Fatalf("projected %+v (dlq %d)\nwant %+v", p.events, len(dlq.msgs), want)
	}
}

func TestAnalyticsConsumerProjectsAnUnknownRegionAndAnIncompleteEstimate(t *testing.T) {
	data := decisionData()
	for _, k := range []string{"nodeRegionId", "inRegion", "estimatedCostCents", "groupNo"} {
		delete(data, k)
	}
	data["costComplete"] = false
	p, _ := handleOne(t, ownEvent(t, "FulfillmentSourcingDecided", cloudevents.StreamAnalytics, "asg-1", data))
	if len(p.events) != 1 {
		t.Fatal("the decision must be projected")
	}
	d := p.events[0]
	if d.GroupNo != 0 || d.Decision.InRegion != nil || d.Decision.EstimatedCostCents != nil || d.Decision.CostComplete || d.Decision.NodeRegionID != "" {
		t.Fatalf("optional fields must stay absent: %+v", d)
	}
}

func TestAnalyticsConsumerProjectsQueuedAndUnsourceableOnTheEventTime(t *testing.T) {
	cases := []struct {
		name, stream string
		data         map[string]any
		kind         report.Kind
		groupNo      int
	}{
		{"FulfillmentQueued", cloudevents.StreamAnalytics, map[string]any{"assignmentId": "asg-1", "orderId": "ord-1", "groupNo": 2,
			"promiseId": "p", "deliverByLatest": decidedRFC, "lines": []any{}}, report.KindFulfillmentQueued, 2},
		{"FulfillmentUnsourceable", cloudevents.StreamEvents, map[string]any{"assignmentId": "asg-1", "orderId": "ord-1", "groupNo": 2,
			"promiseId": "p", "excludedNodes": []any{}, "reason": "NO_CANDIDATE"}, report.KindFulfillmentUnsourceable, 2},
	}
	for _, c := range cases {
		p, dlq := handleOne(t, ownEvent(t, c.name, c.stream, "asg-1", c.data))
		want := report.Event{Kind: c.kind, EventID: "evt-" + c.name, At: anEventTime, AssignmentID: "asg-1", OrderID: "ord-1",
			GroupNo: c.groupNo, OccurredAt: anEventTime}
		if len(dlq.msgs) != 0 || len(p.events) != 1 || !reflect.DeepEqual(p.events[0], want) {
			t.Errorf("%s projected %+v\nwant %+v", c.name, p.events, want)
		}
	}
}

// poison is one deterministic failure the consumer must dead-letter once.
type poison struct {
	name string
	msg  func(t *testing.T) []byte
	errs []error
}

func poisonCases() []poison {
	with := func(k string, v any) map[string]any { d := decisionData(); d[k] = v; return d }
	decided := func(t *testing.T, subject string, d map[string]any) []byte {
		return ownEvent(t, "FulfillmentSourcingDecided", cloudevents.StreamAnalytics, subject, d)
	}
	return []poison{
		{"undecodable timestamp", func(t *testing.T) []byte { return decided(t, "asg-1", with("decidedAt", "yesterday")) }, nil},
		{"subject is not the aggregate", func(t *testing.T) []byte { return decided(t, "asg-9", decisionData()) }, nil},
		{"a required field missing", func(t *testing.T) []byte { return decided(t, "asg-1", with("nodeId", "")) }, nil},
		{"cents without costComplete", func(t *testing.T) []byte { return decided(t, "asg-1", with("costComplete", false)) }, nil},
		{"the store rejects it", func(t *testing.T) []byte { return decided(t, "asg-1", decisionData()) },
			[]error{fmt.Errorf("%w: check violation", report.ErrRejected)}},
	}
}

func TestAnalyticsConsumerDeadLettersPoisonOnce(t *testing.T) {
	for _, c := range poisonCases() {
		p, dlq := &fakeProjection{errs: c.errs}, &recordedDLQ{}
		value := c.msg(t)
		msg := kafkago.Message{Topic: OwnAnalyticsTopic, Partition: 2, Offset: 7, Key: []byte("asg-1"), Value: value}
		if err := analyticsConsumer(p, dlq, nil).HandleMessage(context.Background(), msg); err != nil {
			t.Fatalf("%s: poison must be dead-lettered and committed past, got %v", c.name, err)
		}
		if len(dlq.msgs) != 1 || len(p.events) != 0 {
			t.Fatalf("%s: dlq=%d applied=%d, want exactly one dead letter", c.name, len(dlq.msgs), len(p.events))
		}
		d := dlq.msgs[0]
		if d.Topic != AnalyticsDLQTopic || !bytes.Equal(d.Value, value) || string(d.Key) != "asg-1" || dlqHeader(d, "x-dlq-source-topic") != OwnAnalyticsTopic {
			t.Errorf("%s: dead letter = topic %s key %s headers %v", c.name, d.Topic, d.Key, d.Headers)
		}
		if dlqHeader(d, "x-dlq-error") == "" {
			t.Errorf("%s: the dead letter must carry the cause", c.name)
		}
	}
}

func dlqHeader(m kafkago.Message, key string) string {
	for _, h := range m.Headers {
		if h.Key == key {
			return string(h.Value)
		}
	}
	return ""
}

func TestAnalyticsConsumerTransientFailureIsReturnedNotDeadLettered(t *testing.T) {
	p, dlq := &fakeProjection{errs: []error{errors.New("connection refused")}}, &recordedDLQ{}
	msg := kafkago.Message{Value: ownEvent(t, "FulfillmentSourcingDecided", cloudevents.StreamAnalytics, "asg-1", decisionData())}
	if err := analyticsConsumer(p, dlq, nil).HandleMessage(context.Background(), msg); err == nil {
		t.Fatal("a transient failure must be returned for the loop to retry")
	}
	if len(dlq.msgs) != 0 {
		t.Fatal("a transient failure must never be dead-lettered")
	}
}

func TestAnalyticsConsumerFailedDeadLetterIsRetried(t *testing.T) {
	msg := kafkago.Message{Value: ownEvent(t, "FulfillmentSourcingDecided", cloudevents.StreamAnalytics, "asg-9", decisionData())}
	err := analyticsConsumer(&fakeProjection{}, &recordedDLQ{err: errors.New("broker down")}, nil).HandleMessage(context.Background(), msg)
	if err == nil || !strings.Contains(err.Error(), AnalyticsDLQTopic) {
		t.Fatalf("a failed dead-letter write must be returned (so the message is retried), got %v", err)
	}
}

func TestAnalyticsConsumerRunRetriesTransientFailuresForever(t *testing.T) {
	down := errors.New("down")
	p, dlq := &fakeProjection{errs: []error{down, down, down, down, down, down, nil}}, &recordedDLQ{}
	c := analyticsConsumer(p, dlq, nil)
	rec := &loopRecorder{}
	c.Reader = &scriptedReader{rec: rec, msgs: []kafkago.Message{{Offset: 1,
		Value: ownEvent(t, "FulfillmentSourcingDecided", cloudevents.StreamAnalytics, "asg-1", decisionData())}}}
	var mu sync.Mutex
	var sleeps []time.Duration
	c.Retry = RetryPolicy{Initial: time.Millisecond, Max: 4 * time.Millisecond}
	c.sleep = func(_ context.Context, d time.Duration) error {
		mu.Lock()
		sleeps = append(sleeps, d)
		mu.Unlock()
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- c.Run(ctx) }()
	waitFor(t, 5*time.Second, func() bool { return slices.Contains(rec.log(), "commit:1") })
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run = %v, want context.Canceled", err)
	}
	if p.applied() != 1 || len(dlq.msgs) != 0 {
		t.Fatalf("applied=%d dlq=%d: six transient failures must be retried past the domain cap, never dead-lettered", p.applied(), len(dlq.msgs))
	}
	want := []time.Duration{time.Millisecond, 2 * time.Millisecond, 4 * time.Millisecond, 4 * time.Millisecond, 4 * time.Millisecond, 4 * time.Millisecond}
	mu.Lock()
	defer mu.Unlock()
	if !reflect.DeepEqual(sleeps, want) {
		t.Fatalf("backoff = %v, want the capped sequence %v", sleeps, want)
	}
}
