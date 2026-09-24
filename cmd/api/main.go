// Command api serves the Connect/HTTP API.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/Zefanrakh/kue-preorder/internal/platform/config"
	"github.com/Zefanrakh/kue-preorder/internal/platform/db"
	"github.com/Zefanrakh/kue-preorder/internal/platform/httpserver"
	"github.com/Zefanrakh/kue-preorder/internal/platform/log"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "api: %v\n", err)
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

	database, err := db.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer database.Close()

	mux := http.NewServeMux()
	mux.Handle("GET /healthz", httpserver.Healthz())
	mux.Handle("GET /readyz", httpserver.Readyz(database, logger))

	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", cfg.HTTPAddr())
	if err != nil {
		return fmt.Errorf("listen on %s: %w", cfg.HTTPAddr(), err)
	}
	logger.Info("api listening", slog.String("addr", ln.Addr().String()), slog.String("env", string(cfg.AppEnv)))

	srv := httpserver.New(httpserver.CorrelationID(mux), logger)
	if err := httpserver.Run(ctx, srv, ln); err != nil {
		return err
	}
	logger.Info("api stopped")
	return nil
}
