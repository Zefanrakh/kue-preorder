// Command worker runs background jobs. River and the job handlers arrive in
// M2 (docs/architecture.md §21); until then it only validates configuration
// and waits for a shutdown signal.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/Zefanrakh/kue-preorder/internal/platform/config"
	"github.com/Zefanrakh/kue-preorder/internal/platform/log"
)

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
	logger := log.New(os.Stdout, cfg.LogLevel())

	logger.Info("worker started; no jobs registered yet", slog.String("env", string(cfg.AppEnv)))
	<-ctx.Done()
	logger.Info("worker stopped")
	return nil
}
