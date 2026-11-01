package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	inboundhttp "github.com/claudioed/order-sourcing/internal/adapters/inbound/http"
)

func serveUntilSignal(ctx context.Context, logger *slog.Logger, httpServer *http.Server, readiness *inboundhttp.Readiness,
	background []*worker, closeConsumers, closeSweep func(), relay *worker, closeRelay func(),
) error {
	errCh := make(chan error, 1)
	go func() {
		logger.Info("http server listening", "addr", httpServer.Addr)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()
	stopAll := func() { closeConsumers(); closeSweep(); closeRelay() }

	sigCtx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	died := watchWorkers(append([]*worker{relay}, background...)...)
	select {
	case err := <-errCh:
		stopAll()
		return err
	case name := <-died:
		stopAll()
		return fmt.Errorf("%s stopped unexpectedly: exiting so the orchestrator restarts the service", name)
	case <-sigCtx.Done():
	}

	// Drain: not-ready first so the load balancer stops routing, then close the listener,
	// then stop the workers and wait for them.
	readiness.SetNotReady()
	if delay := shutdownDrainDelay(logger); delay > 0 {
		logger.Info("shutdown: readiness flipped to not-ready; waiting for traffic to drain", "drain_delay", delay.String())
		time.Sleep(delay)
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	err := httpServer.Shutdown(shutdownCtx)

	closeConsumers()
	closeSweep()
	for _, w := range background {
		await(logger, w)
	}
	closeRelay()
	await(logger, relay)
	return err
}

func watchWorkers(workers ...*worker) <-chan string {
	died := make(chan string, len(workers))
	for _, w := range workers {
		if w == nil {
			continue
		}
		go func() {
			<-w.done
			died <- w.name
		}()
	}
	return died
}

func await(logger *slog.Logger, w *worker) {
	if w == nil {
		return
	}
	select {
	case <-w.done:
	case <-time.After(workerDrainTimeout):
		logger.Warn("worker did not stop before the shutdown drain deadline", "worker", w.name)
	}
}

func shutdownDrainDelay(logger *slog.Logger) time.Duration {
	raw := os.Getenv("SHUTDOWN_DRAIN_DELAY")
	if raw == "" {
		return defaultShutdownDrain
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d < 0 {
		logger.Warn("ignoring invalid SHUTDOWN_DRAIN_DELAY", "value", raw, "default", defaultShutdownDrain.String())
		return defaultShutdownDrain
	}
	return d
}
