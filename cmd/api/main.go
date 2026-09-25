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
	"time"

	"connectrpc.com/connect"
	"connectrpc.com/otelconnect"
	"go.opentelemetry.io/otel/trace"

	"github.com/Zefanrakh/kue-preorder/api/gen/go/kuepreorder/catalog/v1/catalogv1connect"
	"github.com/Zefanrakh/kue-preorder/api/gen/go/kuepreorder/identity/v1/identityv1connect"
	"github.com/Zefanrakh/kue-preorder/internal/catalog"
	catalogrpc "github.com/Zefanrakh/kue-preorder/internal/catalog/connect"
	catalogpg "github.com/Zefanrakh/kue-preorder/internal/catalog/postgres"
	"github.com/Zefanrakh/kue-preorder/internal/identity"
	identityrpc "github.com/Zefanrakh/kue-preorder/internal/identity/connect"
	identitypg "github.com/Zefanrakh/kue-preorder/internal/identity/postgres"
	"github.com/Zefanrakh/kue-preorder/internal/platform/clock"
	"github.com/Zefanrakh/kue-preorder/internal/platform/config"
	"github.com/Zefanrakh/kue-preorder/internal/platform/db"
	"github.com/Zefanrakh/kue-preorder/internal/platform/httpserver"
	"github.com/Zefanrakh/kue-preorder/internal/platform/log"
	"github.com/Zefanrakh/kue-preorder/internal/platform/telemetry"
)

const (
	// maxRequestBytes bounds a single Connect request message.
	maxRequestBytes = 1 << 20
	// telemetryFlushTimeout bounds exporting the last spans and Sentry events.
	telemetryFlushTimeout = 5 * time.Second
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
	baseLogger := log.New(os.Stdout, cfg.LogLevel())
	tel, err := telemetry.Setup(ctx, telemetry.Config{
		Service:      "api",
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

	if err := serve(ctx, cfg, logger, tel); err != nil {
		// Logged at error level so a failed start or crash also reaches Sentry.
		logger.ErrorContext(ctx, "api stopped with an error", slog.Any("error", err))
		return err
	}
	logger.Info("api stopped")
	return nil
}

func serve(ctx context.Context, cfg config.Config, logger *slog.Logger, tel *telemetry.Telemetry) error {
	database, err := db.Open(ctx, cfg.DatabaseURL, db.WithTracerProvider(tel.TracerProvider()))
	if err != nil {
		return err
	}
	defer database.Close()

	keys, err := identity.NewJWKS(ctx, cfg.JWKSURL, logger)
	if err != nil {
		return err
	}
	identityRepo := identitypg.NewRepository(database.Pool())
	tenants, err := identity.ResolveSingleTenant(ctx, identityRepo)
	if err != nil {
		return fmt.Errorf("resolve tenant: %w", err)
	}

	identitySvc := identity.NewService(identityRepo, tenants)
	catalogRepo := catalogpg.NewRepository(database)
	handler, err := newHandler(handlerDeps{
		logger:         logger,
		tracerProvider: tel.TracerProvider(),
		verifier:       identity.NewTokenVerifier(keys, cfg.AuthIssuer(), clock.Real{}),
		identity:       identitySvc,
		catalog:        catalog.NewService(catalogRepo, identitySvc, clock.Real{}),
		storefront:     catalog.NewStorefront(catalogRepo, tenants),
		db:             database,
	})
	if err != nil {
		return err
	}

	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", cfg.HTTPAddr())
	if err != nil {
		return fmt.Errorf("listen on %s: %w", cfg.HTTPAddr(), err)
	}
	logger.Info("api listening",
		slog.String("addr", ln.Addr().String()),
		slog.String("env", string(cfg.AppEnv)),
		slog.String("tenant_id", tenants.TenantID(ctx).String()))
	return httpserver.Run(ctx, httpserver.New(handler, logger), ln)
}

// handlerDeps is what the HTTP handler needs; tests pass fakes.
type handlerDeps struct {
	logger         *slog.Logger
	tracerProvider trace.TracerProvider
	verifier       identityrpc.Verifier
	identity       *identity.Service
	catalog        *catalog.Service
	storefront     *catalog.Storefront
	db             httpserver.Pinger
}

// newHandler builds every route with the middleware order production uses:
//
//	CorrelationID → Recover → mux
//	Connect: tracing → auth → handler; a panic becomes Internal
//
// Tracing runs before auth so rejected requests are traced too.
func newHandler(d handlerDeps) (http.Handler, error) {
	tracing, err := otelconnect.NewInterceptor(
		otelconnect.WithTracerProvider(d.tracerProvider),
		otelconnect.WithoutMetrics(),
	)
	if err != nil {
		return nil, fmt.Errorf("create tracing interceptor: %w", err)
	}
	connectOpts := []connect.HandlerOption{
		connect.WithInterceptors(tracing, identityrpc.NewAuthInterceptor(d.verifier, d.logger)),
		httpserver.ConnectRecover(d.logger),
		connect.WithReadMaxBytes(maxRequestBytes),
	}

	mux := http.NewServeMux()
	mux.Handle("GET /healthz", httpserver.Healthz())
	mux.Handle("GET /readyz", httpserver.Readyz(d.db, d.logger))
	mux.Handle(identityv1connect.NewIdentityServiceHandler(identityrpc.NewHandler(d.identity, d.logger), connectOpts...))
	mux.Handle(catalogv1connect.NewCatalogAdminServiceHandler(catalogrpc.NewHandler(d.catalog, d.logger), connectOpts...))
	mux.Handle(catalogv1connect.NewStorefrontServiceHandler(catalogrpc.NewStorefrontHandler(d.storefront, d.logger), connectOpts...))

	return httpserver.CorrelationID(httpserver.Recover(d.logger, mux)), nil
}
