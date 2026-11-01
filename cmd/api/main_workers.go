package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	inboundkafka "github.com/claudioed/order-sourcing/internal/adapters/inbound/kafka"
	outboundkafka "github.com/claudioed/order-sourcing/internal/adapters/outbound/kafka"
	outboxrelay "github.com/claudioed/order-sourcing/internal/adapters/outbound/outbox"
	"github.com/claudioed/order-sourcing/internal/application/usecases"
	"github.com/claudioed/order-sourcing/internal/domain/assignment"
)

// consumerGroupEnvs lists the mandatory consumer-group variables, one per consumed topic.
func consumerGroupEnvs(h inboundkafka.Handlers) []string {
	var out []string
	for _, t := range h.Topics() {
		out = append(out, t.GroupEnv)
	}
	return out
}

// worker is one background goroutine the process supervises.
type worker struct {
	name string
	done chan struct{}
}

func startConsumers(svc services, rt runtimeEnv, logger *slog.Logger) ([]*worker, func()) {
	ctx, stop := context.WithCancel(context.Background())
	var (
		workers []*worker
		closers []func() error
	)
	for _, th := range svc.handlers.Topics() {
		group := rt.groups[th.GroupEnv]
		c := inboundkafka.NewConsumer(th.Name, rt.brokers, th.Topic, group, th.Handlers, logger)
		w := &worker{name: th.Name + " consumer", done: make(chan struct{})}
		workers = append(workers, w)
		closers = append(closers, c.Close)
		go func() {
			defer close(w.done)
			logger.Info("consumer running", "consumer", c.Name, "topic", c.Topic, "group_id", group, "brokers", rt.brokers)
			if err := c.Run(ctx); !errors.Is(err, context.Canceled) {
				logger.Error("consumer stopped", "consumer", c.Name, "error", err)
			}
		}()
	}
	return workers, func() {
		stop()
		for _, closeFn := range closers {
			_ = closeFn()
		}
	}
}

func parseRelayInterval(raw string, logger *slog.Logger) time.Duration {
	if raw == "" {
		return outboxrelay.DefaultInterval
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		if secs, serr := strconv.ParseFloat(raw, 64); serr == nil {
			d, err = time.Duration(secs*float64(time.Second)), nil
		}
	}
	if err != nil || d <= 0 {
		logger.Warn("invalid OUTBOX_RELAY_INTERVAL; using the default", "value", raw, "default", outboxrelay.DefaultInterval)
		return outboxrelay.DefaultInterval
	}
	return d
}

func outboxMaxAttempts(raw string, logger *slog.Logger) int {
	if raw == "" {
		return outboxrelay.DefaultMaxAttempts
	}
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || n < 0 {
		logger.Warn("invalid OUTBOX_MAX_ATTEMPTS; using the default", "value", raw, "default", outboxrelay.DefaultMaxAttempts)
		return outboxrelay.DefaultMaxAttempts
	}
	return n
}

func startOutboxRelay(store outboxrelay.Store, brokers []string, logger *slog.Logger) (*worker, func(), error) {
	if len(brokers) == 0 {
		return nil, nil, errMissingBrokers
	}
	sink := outboundkafka.NewRelaySink(brokers)
	interval := parseRelayInterval(os.Getenv("OUTBOX_RELAY_INTERVAL"), logger)
	maxAttempts := outboxMaxAttempts(os.Getenv("OUTBOX_MAX_ATTEMPTS"), logger)
	relay := outboxrelay.NewOutboxRelay(store, sink, logger, outboxrelay.WithInterval(interval), outboxrelay.WithMaxAttempts(maxAttempts))

	ctx, stop := context.WithCancel(context.Background())
	w := &worker{name: "outbox-relay", done: make(chan struct{})}
	go func() {
		defer close(w.done)
		defer func() {
			if err := sink.Close(); err != nil {
				logger.Error("kafka relay sink close failed", "error", err)
			}
		}()
		logger.Info("outbox relay running", "publisher", "kafka", "interval", interval, "max_attempts", maxAttempts,
			"topics", []string{outboundkafka.TopicEvents, outboundkafka.TopicAnalytics})
		if err := relay.Run(ctx); !errors.Is(err, context.Canceled) {
			logger.Error("outbox relay stopped", "error", err)
		}
	}()
	return w, stop, nil
}

func parseSweepInterval(raw string, logger *slog.Logger) time.Duration {
	if raw == "" {
		return defaultSweepInterval
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		logger.Warn("invalid SWEEP_INTERVAL; using the default", "value", raw, "default", defaultSweepInterval)
		return defaultSweepInterval
	}
	return d
}

// startSweep runs the virtual-queue timer: each tick releases the due assignments and walks
// every page of queued ones to re-optimise them.
func startSweep(sweep *usecases.SweepQueue, logger *slog.Logger) (*worker, func()) {
	interval := parseSweepInterval(os.Getenv("SWEEP_INTERVAL"), logger)
	ctx, stop := context.WithCancel(context.Background())
	w := &worker{name: "queue-sweep", done: make(chan struct{})}
	go func() {
		defer close(w.done)
		logger.Info("queue sweep running", "interval", interval)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			sweepOnce(ctx, sweep, logger)
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return w, stop
}

func sweepOnce(ctx context.Context, sweep *usecases.SweepQueue, logger *slog.Logger) {
	var after assignment.ID
	for ctx.Err() == nil {
		res, err := sweep.Execute(ctx, after, defaultSweepPageSize)
		if err != nil && ctx.Err() == nil {
			logger.ErrorContext(ctx, "queue sweep pass failed", "error", err)
		}
		if res.Released+res.Reoptimised > 0 {
			logger.InfoContext(ctx, "queue sweep", "released", res.Released, "reoptimised", res.Reoptimised, "skipped", res.Skipped)
		}
		if res.Next == "" {
			return
		}
		after = res.Next
	}
}
