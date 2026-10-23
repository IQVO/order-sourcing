package kafka

import (
	"context"
	"errors"
	"testing"
	"time"

	kafkago "github.com/segmentio/kafka-go"
)

// scriptedDLQ writes fail with given errors (consumed one per call); the last
// error repeats. It records every attempt.
type scriptedDLQ struct {
	errs []error
	atom []kafkago.Message
}

func (d *scriptedDLQ) WriteMessages(_ context.Context, msgs ...kafkago.Message) error {
	d.atom = append(d.atom, msgs...)
	if len(d.errs) == 0 {
		return nil
	}
	err := d.errs[0]
	d.errs = d.errs[1:]
	return err
}

func (d *scriptedDLQ) Close() error { return nil }

func notReadyErr() error { return kafkago.UnknownTopicOrPartition }

// TestWriteDLQRetriesOnlyWhileTheTopicIsNotReady pins writeDLQ's contract: an
// error that says the auto-created DLQ topic has no leader yet is retried, any
// other error returns at once, and a cancelled context stops the retry with the
// joined errors.
func TestWriteDLQRetriesOnlyWhileTheTopicIsNotReady(t *testing.T) {
	// Not ready once, then written.
	w := &scriptedDLQ{errs: []error{notReadyErr()}}
	if err := writeDLQ(context.Background(), w, kafkago.Message{Topic: "t.dlq"}); err != nil {
		t.Fatalf("writeDLQ = %v, want nil after the topic appears", err)
	}
	if len(w.atom) != 2 {
		t.Fatalf("attempts = %d, want 2 (not ready, then written)", len(w.atom))
	}

	// Any other error is returned at once, without retry.
	boom := errors.New("boom")
	w = &scriptedDLQ{errs: []error{boom}}
	if err := writeDLQ(context.Background(), w, kafkago.Message{}); !errors.Is(err, boom) {
		t.Fatalf("writeDLQ = %v, want the immediate error", err)
	}
	if len(w.atom) != 1 {
		t.Fatalf("attempts = %d, want 1", len(w.atom))
	}

	// A cancelled context stops the bounded wait and joins both errors.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	w = &scriptedDLQ{errs: []error{notReadyErr()}}
	err := writeDLQ(ctx, w, kafkago.Message{})
	if !errors.Is(err, context.Canceled) || !errors.Is(err, kafkago.UnknownTopicOrPartition) {
		t.Fatalf("writeDLQ = %v, want the joined cancel and last write error", err)
	}
}

// TestIsTopicNotReady pins which write errors count as "the auto-created topic
// is not up yet" — the only ones worth retrying.
func TestIsTopicNotReady(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"unknown topic", kafkago.UnknownTopicOrPartition, true},
		{"no leader", kafkago.LeaderNotAvailable, true},
		{"wrapped unknown topic", errors.Join(kafkago.UnknownTopicOrPartition), true},
		{"other error", errors.New("boom"), false},
		{"per-partition all not ready", kafkago.WriteErrors{nil, kafkago.UnknownTopicOrPartition}, true},
		{"per-partition one hard failure", kafkago.WriteErrors{nil, errors.New("boom")}, false},
		{"empty write errors", kafkago.WriteErrors{}, false},
	}
	for _, c := range cases {
		if got := isTopicNotReady(c.err); got != c.want {
			t.Errorf("%s: isTopicNotReady = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestPublishDeadLetterPreservesTheMessageAndAddsContext proves the dead-letter
// wire shape: the original key, value and headers survive, the x-dlq-* context
// is appended, and the DLQ topic names the message.
func TestPublishDeadLetterPreservesTheMessageAndAddsContext(t *testing.T) {
	original := deadLetterFixture()
	w := &scriptedDLQ{}
	if err := publishDeadLetter(context.Background(), w, "orders.dlq", original, errors.New("handler failed")); err != nil {
		t.Fatalf("publishDeadLetter = %v", err)
	}
	if len(w.atom) != 1 {
		t.Fatalf("published %d messages, want 1", len(w.atom))
	}
	m := w.atom[0]
	if m.Topic != "orders.dlq" {
		t.Errorf("topic = %q, want the DLQ topic", m.Topic)
	}
	if string(m.Key) != "asg-9" || string(m.Value) != "payload" {
		t.Errorf("key/value = %q/%q, want the original", m.Key, m.Value)
	}
	if len(m.Headers) != 6 {
		t.Fatalf("headers = %d, want the original plus five x-dlq-* (%+v)", len(m.Headers), m.Headers)
	}
	if m.Headers[0].Key != "content-type" {
		t.Errorf("the original headers must come first, got %+v", m.Headers[0])
	}
}

// TestPublishDeadLetterRecordsTheFailureContext pins the x-dlq-* header values
// and that the caller's message, its header slice included, is never mutated.
func TestPublishDeadLetterRecordsTheFailureContext(t *testing.T) {
	cause := errors.New("handler failed")
	original := deadLetterFixture()
	before := append([]kafkago.Header(nil), original.Headers...)

	w := &scriptedDLQ{}
	if err := publishDeadLetter(context.Background(), w, "orders.dlq", original, cause); err != nil {
		t.Fatalf("publishDeadLetter = %v", err)
	}
	m := w.atom[0]
	for key, want := range map[string]string{
		"x-dlq-source-topic": "orders", "x-dlq-source-partition": "2", "x-dlq-source-offset": "41",
		"x-dlq-error": cause.Error(),
	} {
		if got := headerValue(m, key); got != want {
			t.Errorf("header %s = %q, want %q", key, got, want)
		}
	}
	if got := headerValue(m, "x-dlq-failed-at"); got == "" {
		t.Errorf("x-dlq-failed-at = %q, want a timestamp", got)
	} else if _, err := time.Parse(time.RFC3339, got); err != nil {
		t.Errorf("x-dlq-failed-at = %q, want RFC3339: %v", got, err)
	}
	if len(original.Headers) != 1 || original.Headers[0].Key != before[0].Key || original.Topic != "orders" || original.Partition != 2 || original.Offset != 41 {
		t.Errorf("the caller's message was mutated: %+v", original)
	}
}

// deadLetterFixture builds the message a scenario dead-letters, with len ==
// cap headers so publishDeadLetter's append cannot alias the backing array.
func deadLetterFixture() kafkago.Message {
	return kafkago.Message{
		Topic: "orders", Partition: 2, Offset: 41, Key: []byte("asg-9"), Value: []byte("payload"),
		Headers: append(make([]kafkago.Header, 0, 1), kafkago.Header{Key: "content-type", Value: []byte("application/cloudevents+json; charset=UTF-8")}),
	}
}

// headerValue returns the value of one header of a Kafka message.
func headerValue(m kafkago.Message, key string) string {
	for _, h := range m.Headers {
		if h.Key == key {
			return string(h.Value)
		}
	}
	return ""
}
