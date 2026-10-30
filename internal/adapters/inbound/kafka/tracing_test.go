package kafka

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	ce "github.com/cloudevents/sdk-go/v2/event"
	kafkago "github.com/segmentio/kafka-go"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"github.com/claudioed/order-sourcing/internal/adapters/kafka/cloudevents"
)

const publisherTraceparent = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"

// tracedRun feeds one message with the given headers through Consumer.Run and
// returns the span context the handler saw and the consumer's ended spans. The
// handler fails with failWith when it is non-nil (the loop then retries the
// same message; the run stops once a span has ended).
func tracedRun(t *testing.T, headers []kafkago.Header, failWith error) (trace.SpanContext, []sdktrace.ReadOnlySpan) {
	t.Helper()
	tp, prop := otel.GetTracerProvider(), otel.GetTextMapPropagator()
	t.Cleanup(func() { otel.SetTracerProvider(tp); otel.SetTextMapPropagator(prop) })
	recorder := tracetest.NewSpanRecorder()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder)))
	otel.SetTextMapPropagator(propagation.TraceContext{})

	var (
		mu   sync.Mutex
		seen trace.SpanContext
	)
	handlers := map[string]HandlerFunc{cloudevents.Type("assignment", "TestApplied"): func(ctx context.Context, _ ce.Event) error {
		mu.Lock()
		defer mu.Unlock()
		seen = trace.SpanContextFromContext(ctx)
		return failWith
	}}
	value, err := cloudevents.New(cloudevents.Spec{ID: "id-1", Entity: "assignment", EventName: "TestApplied", Subject: "asg-1", Time: t0kafka})
	if err != nil {
		t.Fatal(err)
	}
	rec := &loopRecorder{}
	reader := &scriptedReader{rec: rec, msgs: []kafkago.Message{{Topic: "t", Offset: 1, Value: value, Headers: headers}}}
	c := &Consumer{Name: "probe", Topic: "t", Reader: reader, Handlers: handlers, Logger: quietLogger(),
		sleep: func(context.Context, time.Duration) error { return nil }}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = c.Run(ctx) }()
	waitFor(t, 5*time.Second, func() bool {
		if failWith != nil {
			return len(recorder.Ended()) > 0
		}
		return slices.Contains(rec.log(), "commit:1")
	})
	cancel()

	mu.Lock()
	defer mu.Unlock()
	return seen, recorder.Ended()
}

// The consumer span is a consumer-kind span named "<topic> process" whose parent
// is the publisher's span carried in traceparent, and the handler runs inside it.
func TestConsumerSpanIsParentedToThePublisherTrace(t *testing.T) {
	seen, spans := tracedRun(t, []kafkago.Header{
		{Key: "content-type", Value: []byte("application/cloudevents+json; charset=UTF-8")},
		{Key: "traceparent", Value: []byte(publisherTraceparent)},
	}, nil)

	if len(spans) != 1 {
		t.Fatalf("recorded %d spans, want 1", len(spans))
	}
	span := spans[0]
	if span.SpanKind() != trace.SpanKindConsumer || span.Name() != "t process" {
		t.Errorf("span = %v %q, want consumer \"t process\"", span.SpanKind(), span.Name())
	}
	if got := span.SpanContext().TraceID().String(); got != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Errorf("trace id = %s, want the publisher's", got)
	}
	if got := span.Parent().SpanID().String(); got != "00f067aa0ba902b7" {
		t.Errorf("parent span id = %s, want the publish span's", got)
	}
	if seen.SpanID() != span.SpanContext().SpanID() {
		t.Errorf("handler ran under span %s, want the consumer span %s", seen.SpanID(), span.SpanContext().SpanID())
	}
}

func TestConsumerWithoutTraceHeadersStartsANewTrace(t *testing.T) {
	_, spans := tracedRun(t, nil, nil)
	if len(spans) != 1 {
		t.Fatalf("recorded %d spans, want 1", len(spans))
	}
	if spans[0].Parent().IsValid() {
		t.Errorf("parent = %v, want none for a message without traceparent", spans[0].Parent())
	}
}

func TestConsumerSpanRecordsAHandlerFailure(t *testing.T) {
	_, spans := tracedRun(t, []kafkago.Header{{Key: "traceparent", Value: []byte(publisherTraceparent)}}, errors.New("transient"))
	if len(spans) == 0 || spans[0].Status().Code != codes.Error {
		t.Fatalf("spans = %v, want an errored consumer span", spans)
	}
}
