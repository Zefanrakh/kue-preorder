// Command worker runs background jobs. River and the job handlers arrive in
// M2 (docs/architecture.md §21); until then it validates configuration, sets
// up telemetry, and waits for a shutdown signal.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Zefanrakh/kue-preorder/internal/platform/config"
	"github.com/Zefanrakh/kue-preorder/internal/platform/log"
	"github.com/Zefanrakh/kue-preorder/internal/platform/telemetry"
)

// telemetryFlushTimeout bounds exporting the last spans and Sentry events.
const telemetryFlushTimeout = 5 * time.Second

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "worker: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := config.Load(os.Getenv)
	if err != nil {
		return err
	}
	baseLogger := log.New(os.Stdout, cfg.LogLevel())
	tel, err := telemetry.Setup(ctx, telemetry.Config{
		Service:      "worker",
		Environment:  string(cfg.AppEnv),
		OTLPEndpoint: cfg.OTLPEndpoint,
		SentryDSN:    cfg.SentryDSN,
	}, baseLogger)
	if err != nil {
		return err
	}
	defer func() {
		flushCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), telemetryFlushTimeout)
		defer cancel()
		if err := tel.Shutdown(flushCtx); err != nil {
			baseLogger.Warn("telemetry shutdown", slog.Any("error", err))
		}
	}()
	logger := tel.Logger(baseLogger)

	logger.Info("worker started; no jobs registered yet", slog.String("env", string(cfg.AppEnv)))
	<-ctx.Done()
	logger.Info("worker stopped")
	return nil
}
