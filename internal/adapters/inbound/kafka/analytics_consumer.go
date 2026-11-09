package kafka

// The analytics projector's consumer (ADR 0013): it reads this context's OWN
// two topics (integration and analytics) under one fixed, env-supplied
// consumer group and folds the three projected event types into the
// analytical model through report.Projection. It shares the at-least-once run loop of the domain
// consumers, with a different delivery policy:
//
//   - not a CloudEvents 1.0 message, or a CloudEvent of a type the projection
//     neither projects nor acknowledges: skipped and committed past, with a
//     sampled WARN;
//   - a projected type whose payload can never be projected (undecodable, the
//     subject is not the aggregate id, a required field missing) or that the
//     store deterministically rejects (report.ErrRejected: Postgres class 22
//     or 23): dead-lettered at once to AnalyticsDLQTopic, then committed past;
//   - any other failure is transient: the SAME message is retried forever
//     with capped exponential backoff and never dead-lettered, because a
//     database outage must not turn into silently missing analytics.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	kafkago "github.com/segmentio/kafka-go"

	"github.com/claudioed/order-sourcing/internal/adapters/kafka/cloudevents"
	"github.com/claudioed/order-sourcing/internal/analytics/report"
)

const (
	// OwnEventsTopic and OwnAnalyticsTopic are this context's own topics, the
	// projector's only sources (it never reads a sibling's topic).
	OwnEventsTopic    = "retailnetwork.order-sourcing.events"
	OwnAnalyticsTopic = "retailnetwork.order-sourcing.analytics"
	// AnalyticsDLQTopic receives the projector's poison messages. It is not
	// "<topic>.dlq": those names belong to the sibling consumers of the two
	// topics, whose dead letters must not mix with the projector's.
	AnalyticsDLQTopic = "retailnetwork.order-sourcing.analytics.dlq"
)

// OwnTopics are the projector's sources, in a fixed order.
func OwnTopics() []string { return []string{OwnEventsTopic, OwnAnalyticsTopic} }

// skipWarnEvery bounds the WARN lines for skipped messages: the first skip is
// logged, then at most one line per interval carrying the number suppressed.
func skipWarnEvery() time.Duration { return time.Minute }

// AnalyticsConsumer is the projector's consumer; see the file comment for the
// outcome of each message.
type AnalyticsConsumer struct {
	Reader     Reader
	Projection report.Projection
	DLQ        DeadLetterWriter
	// Topic names a span when a message carries no topic (the first source).
	Topic    string
	DLQTopic string
	Logger   *slog.Logger
	Retry    RetryPolicy
	// Now is the clock of the skip-warning sampler (time.Now when nil).
	Now func() time.Time

	skipMu sync.Mutex
	skips  *skipSampler
	sleep  sleepFunc // test hook; nil => real, ctx-cancellable sleep
}

// NewAnalyticsConsumer constructs the consumer of topics (one group member
// subscribed to every topic) under groupID (an env-configured value from the
// composition root, never a literal) writing its dead letters to dlqTopic.
// Nothing dials Kafka here.
func NewAnalyticsConsumer(brokers, topics []string, dlqTopic, groupID string, projection report.Projection, logger *slog.Logger) *AnalyticsConsumer {
	return &AnalyticsConsumer{
		Reader:     kafkago.NewReader(groupReaderConfig(brokers, topics, groupID)),
		Projection: projection,
		DLQ:        newDomainDLQWriter(brokers),
		Topic:      topics[0],
		DLQTopic:   dlqTopic,
		Logger:     defaultLogger(logger),
	}
}

// Run consumes until ctx is cancelled or the reader fails. A transient failure
// is retried without a bound (maxAttempts is left at zero on purpose).
func (c *AnalyticsConsumer) Run(ctx context.Context) error {
	loop := consumeLoop{
		reader: c.Reader,
		handle: func(ctx context.Context, msg kafkago.Message) error {
			return processTraced(ctx, c.Topic, msg, func(ctx context.Context) error { return c.HandleMessage(ctx, msg) })
		},
		logger: defaultLogger(c.Logger),
		name:   "analytics consumer",
		retry:  c.Retry,
		sleep:  c.sleep,
	}
	return loop.run(ctx)
}

// Close releases the reader and the dead-letter writer.
func (c *AnalyticsConsumer) Close() error {
	err := c.Reader.Close()
	if c.DLQ != nil {
		err = errors.Join(err, c.DLQ.Close())
	}
	return err
}

// HandleMessage processes one message. A non-nil error is always transient
// (including a failed dead-letter write): the loop retries the message.
func (c *AnalyticsConsumer) HandleMessage(ctx context.Context, msg kafkago.Message) error {
	evt, err := cloudevents.Decode(msg.Value)
	if err != nil {
		c.skipped(ctx, msg, err)
		return nil
	}
	kind, projected := projectedKinds()[evt.Type()]
	if !projected {
		if !acknowledgedTypes()[evt.Type()] {
			c.skipped(ctx, msg, fmt.Errorf("unknown CloudEvents type %q", evt.Type()))
		}
		return nil
	}
	event, err := toEvent(kind, evt)
	if err != nil {
		//dlq:deterministic toEvent is a pure decode+validation of the event bytes (DataAs, subject and required-field checks; no ctx, no I/O), so cancellation cannot produce this error and the same bytes always fail
		return c.deadLetter(ctx, msg, err)
	}
	if _, err := c.Projection.Apply(ctx, event); err != nil {
		if errors.Is(err, report.ErrRejected) {
			//dlq:deterministic only report.ErrRejected reaches here (validation, SQLSTATE class 22/23); every other Apply error, cancellation included, is returned below as transient
			return c.deadLetter(ctx, msg, err)
		}
		return fmt.Errorf("analytics: project %s %s: %w", kind, evt.ID(), err)
	}
	return nil
}

// skipped logs a skipped message, sampled.
func (c *AnalyticsConsumer) skipped(ctx context.Context, msg kafkago.Message, cause error) {
	c.skipMu.Lock()
	if c.skips == nil {
		now := c.Now
		if now == nil {
			now = time.Now
		}
		c.skips = &skipSampler{every: skipWarnEvery(), now: now}
	}
	c.skipMu.Unlock()
	if emit, suppressed := c.skips.allow(); emit {
		defaultLogger(c.Logger).WarnContext(ctx, "analytics: skipping a message that is not a projected CloudEvent "+
			"(further skips are counted, not logged, until the next interval)",
			"error", cause, "topic", msg.Topic, "partition", msg.Partition, "offset", msg.Offset,
			"suppressed_since_last_warning", suppressed)
	}
}

// deadLetter publishes the raw message to the DLQ with x-dlq-* headers, then
// lets the loop commit past it. A DLQ write failure is returned (transient: the
// loop retries the message), so a poison message is never lost.
func (c *AnalyticsConsumer) deadLetter(ctx context.Context, msg kafkago.Message, cause error) error {
	defaultLogger(c.Logger).ErrorContext(ctx, "analytics: dead-lettering a message that can never be projected",
		"error", cause, "dlq_topic", c.DLQTopic, "partition", msg.Partition, "offset", msg.Offset)
	if err := publishDeadLetter(ctx, c.DLQ, c.DLQTopic, msg, cause); err != nil {
		return fmt.Errorf("analytics: publish to dead-letter topic %s: %w", c.DLQTopic, err)
	}
	return nil
}

// skipSampler lets the first call through, then at most one per interval,
// reporting how many were suppressed in between.
type skipSampler struct {
	mu         sync.Mutex
	every      time.Duration
	now        func() time.Time
	last       time.Time
	suppressed int64
}

func (s *skipSampler) allow() (emit bool, suppressed int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	if s.last.IsZero() || now.Sub(s.last) >= s.every {
		suppressed, s.suppressed, s.last = s.suppressed, 0, now
		return true, suppressed
	}
	s.suppressed++
	return false, 0
}
