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
	"github.com/Zefanrakh/kue-preorder/api/gen/go/kuepreorder/orders/v1/ordersv1connect"
	"github.com/Zefanrakh/kue-preorder/api/gen/go/kuepreorder/scheduling/v1/schedulingv1connect"
	"github.com/Zefanrakh/kue-preorder/internal/catalog"
	catalogrpc "github.com/Zefanrakh/kue-preorder/internal/catalog/connect"
	catalogpg "github.com/Zefanrakh/kue-preorder/internal/catalog/postgres"
	"github.com/Zefanrakh/kue-preorder/internal/identity"
	identityrpc "github.com/Zefanrakh/kue-preorder/internal/identity/connect"
	"github.com/Zefanrakh/kue-preorder/internal/identity/otp"
	identitypg "github.com/Zefanrakh/kue-preorder/internal/identity/postgres"
	"github.com/Zefanrakh/kue-preorder/internal/orders"
	ordersrpc "github.com/Zefanrakh/kue-preorder/internal/orders/connect"
	orderspg "github.com/Zefanrakh/kue-preorder/internal/orders/postgres"
	"github.com/Zefanrakh/kue-preorder/internal/payments"
	paymentspg "github.com/Zefanrakh/kue-preorder/internal/payments/postgres"
	"github.com/Zefanrakh/kue-preorder/internal/platform/clock"
	"github.com/Zefanrakh/kue-preorder/internal/platform/config"
	"github.com/Zefanrakh/kue-preorder/internal/platform/db"
	"github.com/Zefanrakh/kue-preorder/internal/platform/httpserver"
	"github.com/Zefanrakh/kue-preorder/internal/platform/log"
	"github.com/Zefanrakh/kue-preorder/internal/platform/ratelimit"
	"github.com/Zefanrakh/kue-preorder/internal/platform/telemetry"
	"github.com/Zefanrakh/kue-preorder/internal/platform/webhook"
	"github.com/Zefanrakh/kue-preorder/internal/platform/whatsapp"
	"github.com/Zefanrakh/kue-preorder/internal/scheduling"
	schedulingrpc "github.com/Zefanrakh/kue-preorder/internal/scheduling/connect"
	schedulingpg "github.com/Zefanrakh/kue-preorder/internal/scheduling/postgres"
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

	provider, err := paymentProvider(cfg)
	if err != nil {
		return err
	}
	deps := wire(database, identity.NewService(identityRepo, tenants), tenants, provider, clock.Real{}, logger)
	deps.tracerProvider = tel.TracerProvider()
	deps.verifier = identity.NewTokenVerifier(keys, cfg.AuthIssuer(), clock.Real{})
	deps.limiter = ratelimit.New(clock.Real{}, rateLimits)
	deps.clientIPHeader = cfg.ClientIPHeader
	if deps.sendSMSHook, err = sendSMSHook(cfg, database, logger); err != nil {
		return err
	}
	handler, err := newHandler(deps)
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

// wire assembles every module over database as production runs them: this
// is the one place allowed to join a module to another's postgres package
// (§5). The caller adds tracing, token verification, and rate limits.
func wire(database *db.DB, identitySvc *identity.Service, tenants identity.TenantResolver, provider payments.Provider, clk clock.Clock, logger *slog.Logger) handlerDeps {
	catalogRepo := catalogpg.NewRepository(database)
	schedulingRepo := schedulingpg.NewRepository(database)
	ordersRepo := orderspg.NewRepository(database)
	paymentsRepo := paymentspg.NewRepository(database)
	return handlerDeps{
		logger:     logger,
		identity:   identitySvc,
		catalog:    catalog.NewService(catalogRepo, identitySvc, clk),
		storefront: catalog.NewStorefront(catalogRepo, tenants),
		scheduling: scheduling.NewService(schedulingRepo, identitySvc, orders.NewReader(ordersRepo), clk),
		checkout: orders.NewCheckout(orders.Deps{
			Catalog: catalog.NewReader(catalogRepo), Schedules: scheduling.NewReader(schedulingRepo),
			Policies: payments.NewReader(paymentsRepo), Repo: ordersRepo, Customers: identitySvc,
			Ledger: payments.NewLedger(paymentsRepo), Provider: provider, Tx: database,
			Tenants: tenants, Clock: clk, Logger: logger,
		}),
		db: database,
	}
}

// sendSMSHook serves Supabase Auth's Send SMS hook when its secret is set
// (§8). Codes go over WhatsApp; without WhatsApp, development logs them and
// every other environment refuses to start.
func sendSMSHook(cfg config.Config, database *db.DB, logger *slog.Logger) (http.Handler, error) {
	var sender otp.Sender
	switch {
	case cfg.HasWhatsApp():
		wa := whatsapp.New(whatsapp.Config{Token: cfg.WhatsAppToken, PhoneNumberID: cfg.WhatsAppPhoneNumberID}, nil)
		sender = otp.WhatsAppSender{Client: wa, Template: cfg.WhatsAppOTPTemplate}
	case cfg.AppEnv == config.EnvDevelopment:
		sender = otp.LogSender{Logger: logger}
	default:
		return nil, fmt.Errorf("APP_ENV=%s sends sign-in codes over WhatsApp: set WHATSAPP_API_TOKEN, WHATSAPP_PHONE_NUMBER_ID, and WHATSAPP_OTP_TEMPLATE", cfg.AppEnv)
	}
	if cfg.SendSMSHookSecret == "" {
		return nil, nil
	}
	verifier, err := webhook.NewStandardVerifier(cfg.SendSMSHookSecret)
	if err != nil {
		return nil, err
	}
	perPhone := ratelimit.New(clock.Real{}, map[string]ratelimit.Rule{otp.Provider: otp.PerPhone})
	return otp.NewHook(verifier, webhook.NewEvents(database), sender, perPhone, clock.Real{}, logger), nil
}

// paymentProvider picks who makes invoices. Only development may pretend;
// Xendit arrives in M2.6, and until then nothing else may take orders.
func paymentProvider(cfg config.Config) (payments.Provider, error) {
	if cfg.AppEnv == config.EnvDevelopment {
		return payments.DevProvider{}, nil
	}
	return nil, fmt.Errorf("APP_ENV=%s needs a payment provider, and Xendit arrives in M2.6: run with APP_ENV=development until then", cfg.AppEnv)
}

// handlerDeps is what the HTTP handler needs; tests pass fakes.
type handlerDeps struct {
	logger         *slog.Logger
	tracerProvider trace.TracerProvider
	verifier       identityrpc.Verifier
	identity       *identity.Service
	catalog        *catalog.Service
	storefront     *catalog.Storefront
	scheduling     *scheduling.Service
	checkout       *orders.Checkout
	limiter        *ratelimit.Limiter
	clientIPHeader string
	sendSMSHook    http.Handler // nil: not served
	db             httpserver.Pinger
}

// rateLimits are the limits on public procedures, per client address
// (§22). Many Indonesian mobile users share one address, so they are
// generous. Staff procedures need a sign-in and have none.
var rateLimits = map[string]ratelimit.Rule{
	// A customer quotes as they edit the cart and pick a time.
	ordersv1connect.CheckoutServiceQuoteOrderProcedure: {Every: time.Second, Burst: 30},
	// An order an hour on average, ten at once: a family ordering for an event.
	ordersv1connect.CustomerOrderServicePlaceOrderProcedure: {Every: 6 * time.Minute, Burst: 10},
	// Opening an order may ask the payment provider for a missing link.
	ordersv1connect.CustomerOrderServiceGetMyOrderProcedure: {Every: time.Second, Burst: 30},
}

// cacheable are the public responses a CDN may keep, and for how long (§22).
var cacheable = map[string]time.Duration{
	catalogv1connect.StorefrontServiceListShopProductsProcedure: time.Minute,
	catalogv1connect.StorefrontServiceGetShopProductProcedure:   time.Minute,
}

// newHandler builds every route with the middleware order production uses:
//
//	CorrelationID → Recover → ClientIP → CacheControl → mux
//	Connect: tracing → rate limit → auth → handler; a panic becomes Internal
//
// Tracing runs first so refused requests are traced too; the rate limit runs
// before auth so a flood costs no token verification.
func newHandler(d handlerDeps) (http.Handler, error) {
	tracing, err := otelconnect.NewInterceptor(
		otelconnect.WithTracerProvider(d.tracerProvider),
		otelconnect.WithoutMetrics(),
	)
	if err != nil {
		return nil, fmt.Errorf("create tracing interceptor: %w", err)
	}
	connectOpts := []connect.HandlerOption{
		connect.WithInterceptors(tracing, d.limiter.Interceptor(), identityrpc.NewAuthInterceptor(d.verifier, d.logger)),
		httpserver.ConnectRecover(d.logger),
		connect.WithReadMaxBytes(maxRequestBytes),
	}

	mux := http.NewServeMux()
	mux.Handle("GET /healthz", httpserver.Healthz())
	mux.Handle("GET /readyz", httpserver.Readyz(d.db, d.logger))
	if d.sendSMSHook != nil {
		// Supabase Auth hands over sign-in codes here, signed (§8, §18).
		mux.Handle("POST /hooks/supabase/send-sms", d.sendSMSHook)
	}
	mux.Handle(identityv1connect.NewIdentityServiceHandler(identityrpc.NewHandler(d.identity, d.logger), connectOpts...))
	mux.Handle(catalogv1connect.NewCatalogAdminServiceHandler(catalogrpc.NewHandler(d.catalog, d.logger), connectOpts...))
	mux.Handle(catalogv1connect.NewStorefrontServiceHandler(catalogrpc.NewStorefrontHandler(d.storefront, d.logger), connectOpts...))
	mux.Handle(schedulingv1connect.NewScheduleAdminServiceHandler(schedulingrpc.NewHandler(d.scheduling, d.logger), connectOpts...))
	mux.Handle(ordersv1connect.NewCheckoutServiceHandler(ordersrpc.NewCheckoutHandler(d.checkout, d.logger), connectOpts...))
	mux.Handle(ordersv1connect.NewCustomerOrderServiceHandler(ordersrpc.NewCustomerOrderHandler(d.checkout, d.logger), connectOpts...))

	routes := httpserver.ClientIP(d.clientIPHeader, httpserver.CacheControl(cacheable, mux))
	return httpserver.CorrelationID(httpserver.Recover(d.logger, routes)), nil
}
