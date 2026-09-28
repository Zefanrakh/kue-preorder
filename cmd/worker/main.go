// Command worker runs the background jobs (docs/architecture.md §21) on
// River: it publishes the outbox, sweeps orders past their deadlines, and
// bills balances.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"go.opentelemetry.io/otel/trace"

	"github.com/Zefanrakh/kue-preorder/cmd/internal/app"
	"github.com/Zefanrakh/kue-preorder/internal/identity"
	identitypg "github.com/Zefanrakh/kue-preorder/internal/identity/postgres"
	"github.com/Zefanrakh/kue-preorder/internal/orders"
	orderspg "github.com/Zefanrakh/kue-preorder/internal/orders/postgres"
	ordersworker "github.com/Zefanrakh/kue-preorder/internal/orders/worker"
	"github.com/Zefanrakh/kue-preorder/internal/payments"
	paymentspg "github.com/Zefanrakh/kue-preorder/internal/payments/postgres"
	"github.com/Zefanrakh/kue-preorder/internal/platform/clock"
	"github.com/Zefanrakh/kue-preorder/internal/platform/config"
	"github.com/Zefanrakh/kue-preorder/internal/platform/db"
	"github.com/Zefanrakh/kue-preorder/internal/platform/jobs"
	"github.com/Zefanrakh/kue-preorder/internal/platform/log"
	"github.com/Zefanrakh/kue-preorder/internal/platform/outbox"
	"github.com/Zefanrakh/kue-preorder/internal/platform/telemetry"
)

const (
	// telemetryFlushTimeout bounds exporting the last spans and Sentry events.
	telemetryFlushTimeout = 5 * time.Second
	// stopTimeout bounds waiting for running jobs at shutdown; River hands
	// back what is left, to be worked again.
	stopTimeout = 20 * time.Second
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

	if err := work(ctx, cfg, logger, tel.TracerProvider()); err != nil {
		// Logged at error level so a failed start or crash also reaches Sentry.
		logger.ErrorContext(ctx, "worker stopped with an error", slog.Any("error", err))
		return err
	}
	logger.Info("worker stopped")
	return nil
}

func work(ctx context.Context, cfg config.Config, logger *slog.Logger, tp trace.TracerProvider) error {
	database, err := db.Open(ctx, cfg.DatabaseURL, db.WithTracerProvider(tp))
	if err != nil {
		return err
	}
	defer database.Close()
	tenants, err := identity.ResolveSingleTenant(ctx, identitypg.NewRepository(database.Pool()))
	if err != nil {
		return fmt.Errorf("resolve tenant: %w", err)
	}
	provider, err := app.PaymentProvider(cfg)
	if err != nil {
		return err
	}
	client, publisher, err := wire(database, tenants, provider, clock.Real{}, logger, tp)
	if err != nil {
		return err
	}

	if err := client.Start(ctx); err != nil {
		return fmt.Errorf("start jobs: %w", err)
	}
	published := make(chan struct{})
	go func() {
		defer close(published)
		publisher.Run(ctx)
	}()
	logger.Info("worker started", slog.String("env", string(cfg.AppEnv)), slog.String("tenant_id", tenants.TenantID(ctx).String()))

	<-ctx.Done()
	<-published
	stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), stopTimeout)
	defer cancel()
	if err := client.Stop(stopCtx); err != nil {
		return fmt.Errorf("stop jobs: %w", err)
	}
	return nil
}

// wire assembles the jobs over database as production runs them; like the
// api's wire, it is a composition root and may join modules to each other's
// postgres packages (§5).
func wire(database *db.DB, tenants identity.TenantResolver, provider payments.Provider, clk clock.Clock, logger *slog.Logger, tp trace.TracerProvider) (*river.Client[pgx.Tx], *outbox.Publisher, error) {
	ordersRepo := orderspg.NewRepository(database)
	paymentsRepo := paymentspg.NewRepository(database)
	automation := orders.NewAutomation(orders.AutomationDeps{
		Repo: ordersRepo, Ledger: payments.NewLedger(paymentsRepo), Policies: payments.NewReader(paymentsRepo),
		Provider: provider, Tx: database, Clock: clk,
	})

	workers := river.NewWorkers()
	ordersworker.Register(workers, ordersworker.Deps{Automation: automation, Tenants: tenants, Logger: logger})
	client, err := jobs.NewClient(database, jobs.Config{
		Workers: workers, Periodic: ordersworker.Periodic(), Logger: logger, TracerProvider: tp,
	})
	if err != nil {
		return nil, nil, err
	}
	publisher := outbox.NewPublisher(database, jobs.Dispatcher(client, ordersworker.Subscriptions(), logger), clk, logger)
	return client, publisher, nil
}
