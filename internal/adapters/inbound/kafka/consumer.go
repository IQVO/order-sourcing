package kafka

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	ce "github.com/cloudevents/sdk-go/v2/event"
	kafkago "github.com/segmentio/kafka-go"

	"github.com/claudioed/order-sourcing/internal/adapters/kafka/cloudevents"
	"github.com/claudioed/order-sourcing/internal/application/ports"
	"github.com/claudioed/order-sourcing/internal/domain/assignment"
	"github.com/claudioed/order-sourcing/internal/domain/shared"
)

// errBadPayload marks an event whose data cannot be decoded into the fields this
// service relies on. It can never succeed on retry.
var errBadPayload = errors.New("malformed event payload")

// errPoison wraps a deterministic failure so the consume loop dead-letters the
// message at once instead of retrying it.
var errPoison = errors.New("poison message")

// HandlerFunc applies one decoded CloudEvent. A nil return acknowledges it.
type HandlerFunc func(ctx context.Context, e ce.Event) error

// Consumer reads one topic and routes each CloudEvent by its full `type`.
type Consumer struct {
	Name     string
	Topic    string
	Reader   Reader
	Handlers map[string]HandlerFunc
	Logger   *slog.Logger
	Retry    RetryPolicy

	// DLQ receives deterministic poison; nil means poison is logged and skipped.
	DLQ DeadLetterWriter

	sleep sleepFunc // test hook; nil => real, ctx-cancellable sleep
}

// NewConsumer builds a consumer of topic under groupID that dead-letters to <topic>.dlq.
func NewConsumer(name string, brokers []string, topic, groupID string, handlers map[string]HandlerFunc, logger *slog.Logger) *Consumer {
	return &Consumer{
		Name: name, Topic: topic, Reader: kafkago.NewReader(readerConfig(brokers, topic, groupID)),
		Handlers: handlers, Logger: defaultLogger(logger), DLQ: newDomainDLQWriter(brokers),
	}
}

// Run consumes until ctx is done or the reader fails for good.
func (c *Consumer) Run(ctx context.Context) error {
	loop := consumeLoop{
		reader: c.Reader,
		handle: func(ctx context.Context, msg kafkago.Message) error { return c.HandleMessage(ctx, msg.Value) },
		logger: defaultLogger(c.Logger), name: c.Name, retry: c.Retry, sleep: c.sleep,
	}
	if c.DLQ != nil {
		loop.maxAttempts = domainMaxHandlerAttempts
		loop.deadLetter = func(ctx context.Context, msg kafkago.Message, cause error) error {
			return publishDeadLetter(ctx, c.DLQ, c.Topic+DLQSuffix, msg, cause)
		}
	}
	return loop.run(ctx)
}

// Close releases the reader and the DLQ writer.
func (c *Consumer) Close() error {
	err := c.Reader.Close()
	if c.DLQ != nil {
		err = errors.Join(err, c.DLQ.Close())
	}
	return err
}

// neverApplicable are the failures a redelivery cannot cure: the event is about a
// state the aggregate has already left, or about an assignment this service does not own.
func neverApplicable(err error) bool {
	for _, target := range []error{
		ports.ErrNotFound, shared.ErrIllegalTransition, assignment.ErrAssignmentLocked, assignment.ErrReleaseNotPending,
		assignment.ErrStaleRevision, assignment.ErrStaleAssignmentVersion,
	} {
		if errors.Is(err, target) {
			return true
		}
	}
	return false
}

// HandleMessage decodes value and runs the handler of its type. A message that is
// not a CloudEvent or has an unrouted type is skipped; a payload that can never be
// applied is dead-lettered (when a DLQ is set); an event that is stale for the
// aggregate is logged and acknowledged; any other error is returned so the loop retries.
func (c *Consumer) HandleMessage(ctx context.Context, value []byte) error {
	logger := defaultLogger(c.Logger)
	e, err := cloudevents.Decode(value)
	if err != nil {
		logger.WarnContext(ctx, "skipping a message that is not a valid CloudEvent", "consumer", c.Name, "topic", c.Topic, "error", err)
		return nil
	}
	handle, ok := c.Handlers[e.Type()]
	if !ok {
		return nil
	}
	err = handle(ctx, e)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, errBadPayload) || errors.Is(err, shared.ErrValidation):
		if c.DLQ != nil {
			logger.WarnContext(ctx, "dead-lettering an event that can never be applied", "consumer", c.Name, "type", e.Type(), "event_id", e.ID(), "error", err)
			return fmt.Errorf("%w: %w", errPoison, err)
		}
		logger.WarnContext(ctx, "skipping an event that can never be applied", "consumer", c.Name, "type", e.Type(), "event_id", e.ID(), "error", err)
		return nil
	case neverApplicable(err):
		logger.WarnContext(ctx, "skipping an event the aggregate can no longer take", "consumer", c.Name, "type", e.Type(), "event_id", e.ID(), "error", err)
		return nil
	default:
		return err
	}
}

func decodeData[T any](e ce.Event) (T, error) {
	var v T
	if err := e.DataAs(&v); err != nil {
		return v, fmt.Errorf("%w: %v", errBadPayload, err)
	}
	return v, nil
}
