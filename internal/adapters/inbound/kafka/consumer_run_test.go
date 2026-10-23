package kafka

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	ce "github.com/cloudevents/sdk-go/v2/event"
	kafkago "github.com/segmentio/kafka-go"

	"github.com/claudioed/order-sourcing/internal/adapters/kafka/cloudevents"
	"github.com/claudioed/order-sourcing/internal/domain/shared"
)

// recordedDLQ captures what a dead-lettering consumer publishes.
type recordedDLQ struct {
	msgs   []kafkago.Message
	err    error // returned by every WriteMessages call when set
	closed bool
}

func (d *recordedDLQ) WriteMessages(_ context.Context, msgs ...kafkago.Message) error {
	if d.err != nil {
		return d.err
	}
	d.msgs = append(d.msgs, msgs...)
	return nil
}

func (d *recordedDLQ) Close() error {
	d.closed = true
	return nil
}

// TestNewConsumerWiresReaderHandlersAndDLQ pins the constructor's wiring: the
// consumer names itself, reads the topic through a real kafka-go reader (no
// dial happens at construction), keeps the handlers map and prepares the
// domain dead-letter writer.
func TestNewConsumerWiresReaderHandlersAndDLQ(t *testing.T) {
	handlers := map[string]HandlerFunc{"some-type": func(context.Context, ce.Event) error { return nil }}
	c := NewConsumer("orders", []string{"b1:9092"}, "retailnetwork.customer-ordering.events", "group-1", handlers, nil)
	if c.Name != "orders" || c.Topic != "retailnetwork.customer-ordering.events" {
		t.Fatalf("consumer = %q %q", c.Name, c.Topic)
	}
	if c.Reader == nil {
		t.Fatal("the consumer must carry a reader")
	}
	if c.DLQ == nil {
		t.Fatal("the domain consumer must carry a dead-letter writer")
	}
	if _, ok := c.Handlers["some-type"]; !ok {
		t.Fatal("the handlers map must be kept as given")
	}
	if c.Reader.(*kafkago.Reader).Config().GroupID != "group-1" {
		t.Fatal("the group id comes from the caller, never a literal")
	}
	if err := c.Close(); err != nil {
		t.Fatalf("close = %v", err)
	}
}

// TestReaderConfigKeepsExplicitCommitShape pins the reader configuration: the
// GroupID is passed through and CommitInterval stays unset so commits remain
// synchronous inside CommitMessages (offsets move only after a handled message).
func TestReaderConfigKeepsExplicitCommitShape(t *testing.T) {
	cfg := readerConfig([]string{"b:9092"}, "topic-1", "group-x")
	if len(cfg.Brokers) != 1 || cfg.Brokers[0] != "b:9092" || cfg.Topic != "topic-1" || cfg.GroupID != "group-x" {
		t.Fatalf("config = %+v", cfg)
	}
	if cfg.CommitInterval != 0 {
		t.Fatalf("CommitInterval = %v, must stay unset for synchronous commits", cfg.CommitInterval)
	}
}

// TestConsumerRunHandlesCommitsAndStops covers the Run loop end to end over a
// scripted reader: an event of a routed type is applied and committed, an
// unknown type is committed past, and Run returns when ctx is done.
func TestConsumerRunHandlesCommitsAndStops(t *testing.T) {
	applied := 0
	handlers := map[string]HandlerFunc{
		cloudevents.Type("assignment", "TestApplied"): func(context.Context, ce.Event) error {
			applied++
			return nil
		},
	}
	routed, err := cloudevents.New(cloudevents.Spec{ID: "id-1", Entity: "assignment", EventName: "TestApplied", Subject: "asg-1", Time: t0kafka})
	if err != nil {
		t.Fatal(err)
	}
	noise, err := cloudevents.New(cloudevents.Spec{ID: "id-2", Entity: "assignment", EventName: "NeverRouted", Subject: "asg-1", Time: t0kafka})
	if err != nil {
		t.Fatal(err)
	}
	rec := &loopRecorder{}
	reader := &scriptedReader{rec: rec, msgs: []kafkago.Message{
		{Topic: "t", Offset: 1, Value: routed},
		{Topic: "t", Offset: 2, Value: noise},
	}}
	c := &Consumer{Name: "test", Topic: "t", Reader: reader, Handlers: handlers, Logger: quietLogger()}
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = c.Run(ctx) }()
	waitFor(t, time.Second, func() bool {
		return slices.Contains(rec.log(), "commit:2")
	})
	cancel()
	if applied != 1 {
		t.Fatalf("routed handler applied %d times, want exactly 1", applied)
	}
	if got := rec.log(); !slices.Contains(got, "commit:1") || !slices.Contains(got, "commit:2") {
		t.Fatalf("loop log = %v, want both offsets committed", got)
	}
}

// TestConsumerRunDeadLettersPoisonThroughTheDLQ proves the wiring between Run,
// HandleMessage's poison path and publishDeadLetter: an event that can never be
// applied (here shared.ErrValidation) is published to "<topic>.dlq" with the
// x-dlq-* headers, exactly once, and the offset is still committed.
func TestConsumerRunDeadLettersPoisonThroughTheDLQ(t *testing.T) {
	handlers := map[string]HandlerFunc{
		cloudevents.Type("assignment", "TestPoison"): func(context.Context, ce.Event) error {
			return shared.Invalid("quantity must be positive")
		},
	}
	value, err := cloudevents.New(cloudevents.Spec{ID: "id-p", Entity: "assignment", EventName: "TestPoison", Subject: "asg-1", Time: t0kafka})
	if err != nil {
		t.Fatal(err)
	}
	rec := &loopRecorder{}
	dlq := &recordedDLQ{}
	reader := &scriptedReader{rec: rec, msgs: []kafkago.Message{{Topic: "orders", Partition: 3, Offset: 7, Key: []byte("asg-1"), Value: value}}}
	c := &Consumer{Name: "test", Topic: "orders", Reader: reader, Handlers: handlers, Logger: quietLogger(), DLQ: dlq,
		sleep: func(context.Context, time.Duration) error { return nil }}
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = c.Run(ctx) }()
	waitFor(t, 5*time.Second, func() bool {
		return slices.Contains(rec.log(), "commit:7")
	})
	cancel()
	if len(dlq.msgs) != 1 {
		t.Fatalf("dead-lettered %d messages, want exactly 1 (%+v)", len(dlq.msgs), dlq.msgs)
	}
	m := dlq.msgs[0]
	if m.Topic != "orders"+DLQSuffix {
		t.Fatalf("dlq topic = %q, want %q", m.Topic, "orders"+DLQSuffix)
	}
	if string(m.Key) != "asg-1" || string(m.Value) != string(value) {
		t.Fatalf("dlq message = key %q value %q, want the original key and value", m.Key, m.Value)
	}
	for key, want := range map[string]string{
		"x-dlq-source-topic": "orders", "x-dlq-source-partition": "3", "x-dlq-source-offset": "7",
	} {
		if got := headerValue(m, key); got != want {
			t.Errorf("header %s = %q, want %q", key, got, want)
		}
	}
	if got := headerValue(m, "x-dlq-error"); !strings.Contains(got, "quantity must be positive") {
		t.Errorf("x-dlq-error = %q, want the cause", got)
	}
	if got := headerValue(m, "x-dlq-failed-at"); got == "" {
		t.Errorf("x-dlq-failed-at = %q, want a timestamp", got)
	} else if _, err := time.Parse(time.RFC3339, got); err != nil {
		t.Errorf("x-dlq-failed-at = %q, want RFC3339: %v", got, err)
	}
}

// TestConsumerCloseJoinsReaderAndDLQErrors pins Close's contract: both errors
// surface, joined; with no DLQ only the reader's error is returned.
func TestConsumerCloseJoinsReaderAndDLQErrors(t *testing.T) {
	readerErr := errors.New("reader close failed")
	dlqErr := errors.New("dlq close failed")
	c := &Consumer{Reader: &failingCloser{err: readerErr}, DLQ: &failingCloser{err: dlqErr}, Logger: quietLogger()}
	err := c.Close()
	if !errors.Is(err, readerErr) || !errors.Is(err, dlqErr) {
		t.Fatalf("close = %v, want both the reader and the DLQ error", err)
	}
	c = &Consumer{Reader: &failingCloser{err: readerErr}, Logger: quietLogger()}
	if err := c.Close(); !errors.Is(err, readerErr) {
		t.Fatalf("close = %v, want the reader error", err)
	}
}

type failingCloser struct{ err error }

func (f *failingCloser) WriteMessages(context.Context, ...kafkago.Message) error { return nil }
func (f *failingCloser) Close() error                                            { return f.err }
func (f *failingCloser) FetchMessage(context.Context) (kafkago.Message, error) {
	return kafkago.Message{}, nil
}
func (f *failingCloser) CommitMessages(context.Context, ...kafkago.Message) error { return nil }

// t0kafka is a fixed instant for event timestamps in this file.
var t0kafka = time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)

// waitFor polls cond until it holds or the deadline passes.
func waitFor(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
}
