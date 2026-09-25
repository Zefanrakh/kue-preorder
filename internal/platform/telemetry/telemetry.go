// Package telemetry turns on tracing (OpenTelemetry) and error reporting
// (Sentry) for a binary (docs/architecture.md §22). Both stay off unless
// configured, so local runs and tests need no accounts.
//
// Tracing exports spans over OTLP/HTTP to OTEL_EXPORTER_OTLP_ENDPOINT; the SDK
// reads the other OTEL_* variables (headers, service name, sampler) itself.
// Error reporting sends every ERROR log record to Sentry through Logger.
package telemetry

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"

	"github.com/getsentry/sentry-go"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

// Config selects what to turn on. An empty OTLPEndpoint or SentryDSN leaves
// that part off.
type Config struct {
	// Service names the binary: "api" or "worker".
	Service string
	// Environment is APP_ENV.
	Environment  string
	OTLPEndpoint string
	SentryDSN    string

	// sentryTransport replaces Sentry's HTTP transport in tests.
	sentryTransport sentry.Transport
}

// Telemetry holds what Setup started. Shutdown flushes and stops it.
type Telemetry struct {
	tracerProvider trace.TracerProvider
	sdkProvider    *sdktrace.TracerProvider // nil when tracing is off
	sentry         *sentry.Hub              // nil when Sentry is off
}

// Setup starts tracing and error reporting as configured. Warnings from the
// OpenTelemetry SDK, such as an unreachable collector, go to logger.
func Setup(ctx context.Context, cfg Config, logger *slog.Logger) (*Telemetry, error) {
	// Always propagate W3C trace context, so a caller's trace continues here.
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}))

	t := &Telemetry{tracerProvider: noop.NewTracerProvider()}
	if cfg.OTLPEndpoint != "" {
		tp, err := newTracerProvider(ctx, cfg)
		if err != nil {
			return nil, err
		}
		otel.SetErrorHandler(otel.ErrorHandlerFunc(func(err error) {
			// Warn, not Error: an unreachable collector must not flood Sentry.
			logger.Warn("opentelemetry", slog.Any("error", err))
		}))
		otel.SetTracerProvider(tp)
		t.tracerProvider, t.sdkProvider = tp, tp
	}
	if cfg.SentryDSN != "" {
		client, err := sentry.NewClient(sentry.ClientOptions{
			Dsn:              cfg.SentryDSN,
			Environment:      cfg.Environment,
			Release:          version(),
			ServerName:       cfg.Service,
			AttachStacktrace: true,
			DataCollection:   noPersonalData(),
			Transport:        cfg.sentryTransport,
		})
		if err != nil {
			// sentry's error can echo the DSN, which embeds a key.
			return nil, errors.New("set up Sentry: invalid SENTRY_DSN")
		}
		t.sentry = sentry.NewHub(client, sentry.NewScope())
	}
	return t, nil
}

// noPersonalData turns off everything Sentry would collect on its own: user
// info, cookies, headers, bodies, and query parameters. Sentry's defaults
// collect all of them (scrubbed by a denylist); events should carry only
// what we log on purpose.
func noPersonalData() *sentry.DataCollection {
	off := func() *sentry.KeyValueCollectionBehavior {
		return &sentry.KeyValueCollectionBehavior{Mode: sentry.CollectionOff}
	}
	return &sentry.DataCollection{
		UserInfo:    sentry.Set(false),
		Cookies:     off(),
		HTTPHeaders: &sentry.HeaderCollectionConfig{Request: off(), Response: off()},
		HTTPBodies:  []sentry.BodyType{}, // empty, not nil: nil means "all"
		QueryParams: off(),
	}
}

func newTracerProvider(ctx context.Context, cfg Config) (*sdktrace.TracerProvider, error) {
	exporter, err := otlptracehttp.New(ctx) // endpoint and headers from OTEL_EXPORTER_OTLP_*
	if err != nil {
		return nil, fmt.Errorf("create OTLP trace exporter: %w", err)
	}
	// Later options win, so OTEL_SERVICE_NAME and OTEL_RESOURCE_ATTRIBUTES can override ours.
	res, err := resource.New(ctx,
		resource.WithSchemaURL(semconv.SchemaURL),
		resource.WithAttributes(
			semconv.ServiceName(cfg.Service),
			semconv.ServiceVersion(version()),
			semconv.DeploymentEnvironmentNameKey.String(cfg.Environment),
		),
		resource.WithTelemetrySDK(),
		resource.WithFromEnv(),
	)
	if err != nil {
		return nil, fmt.Errorf("describe trace resource: %w", err)
	}
	return sdktrace.NewTracerProvider(sdktrace.WithBatcher(exporter), sdktrace.WithResource(res)), nil
}

// TracerProvider returns the provider instrumentation should use. It is a
// no-op when tracing is off.
func (t *Telemetry) TracerProvider() trace.TracerProvider {
	return t.tracerProvider
}

// Shutdown exports buffered spans and sends queued Sentry events, within
// ctx's deadline.
func (t *Telemetry) Shutdown(ctx context.Context) error {
	var errs []error
	if t.sdkProvider != nil {
		if err := t.sdkProvider.Shutdown(ctx); err != nil {
			errs = append(errs, fmt.Errorf("shut down tracing: %w", err))
		}
	}
	if t.sentry != nil && !t.sentry.FlushWithContext(ctx) {
		errs = append(errs, errors.New("flush Sentry: timed out"))
	}
	return errors.Join(errs...)
}

// version identifies the build: the git commit it was built from, or "dev".
func version() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "dev"
	}
	var revision, modified string
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			revision = s.Value
		case "vcs.modified":
			modified = s.Value
		}
	}
	if revision == "" {
		return "dev"
	}
	if len(revision) > 12 {
		revision = revision[:12]
	}
	if modified == "true" {
		revision += "-dirty"
	}
	return revision
}
