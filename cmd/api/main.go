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

	"connectrpc.com/connect"

	"github.com/Zefanrakh/kue-preorder/api/gen/go/kuepreorder/identity/v1/identityv1connect"
	"github.com/Zefanrakh/kue-preorder/internal/identity"
	identityrpc "github.com/Zefanrakh/kue-preorder/internal/identity/connect"
	identitypg "github.com/Zefanrakh/kue-preorder/internal/identity/postgres"
	"github.com/Zefanrakh/kue-preorder/internal/platform/clock"
	"github.com/Zefanrakh/kue-preorder/internal/platform/config"
	"github.com/Zefanrakh/kue-preorder/internal/platform/db"
	"github.com/Zefanrakh/kue-preorder/internal/platform/httpserver"
	"github.com/Zefanrakh/kue-preorder/internal/platform/log"
)

// maxRequestBytes bounds a single Connect request message.
const maxRequestBytes = 1 << 20

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

	// Identity: every Connect handler shares the auth interceptor.
	keys, err := identity.NewJWKS(ctx, cfg.JWKSURL, logger)
	if err != nil {
		return err
	}
	verifier := identity.NewTokenVerifier(keys, cfg.AuthIssuer(), clock.Real{})
	identityRepo := identitypg.NewRepository(database.Pool())
	tenants, err := identity.ResolveSingleTenant(ctx, identityRepo)
	if err != nil {
		return fmt.Errorf("resolve tenant: %w", err)
	}
	identitySvc := identity.NewService(identityRepo, tenants)
	connectOpts := []connect.HandlerOption{
		connect.WithInterceptors(identityrpc.NewAuthInterceptor(verifier, logger)),
		connect.WithReadMaxBytes(maxRequestBytes),
	}

	mux := http.NewServeMux()
	mux.Handle("GET /healthz", httpserver.Healthz())
	mux.Handle("GET /readyz", httpserver.Readyz(database, logger))
	mux.Handle(identityv1connect.NewIdentityServiceHandler(identityrpc.NewHandler(identitySvc, logger), connectOpts...))

	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", cfg.HTTPAddr())
	if err != nil {
		return fmt.Errorf("listen on %s: %w", cfg.HTTPAddr(), err)
	}
	logger.Info("api listening",
		slog.String("addr", ln.Addr().String()),
		slog.String("env", string(cfg.AppEnv)),
		slog.String("tenant_id", tenants.TenantID(ctx).String()))

	srv := httpserver.New(httpserver.CorrelationID(mux), logger)
	if err := httpserver.Run(ctx, srv, ln); err != nil {
		return err
	}
	logger.Info("api stopped")
	return nil
}
