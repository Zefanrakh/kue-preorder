package jobs

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/riverqueue/river/rivertype"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/Zefanrakh/kue-preorder/internal/platform/log"
)

// A failure alerts (ERROR) only once River gives up; before that it warns.
func TestErrorHandler_AlertsOnTheLastAttempt(t *testing.T) {
	var buf bytes.Buffer
	h := &errorHandler{logger: slog.New(slog.NewJSONHandler(&buf, nil))}
	boom := errors.New("provider unreachable")

	h.HandleError(t.Context(), &rivertype.JobRow{ID: 7, Kind: "create-balance-invoice", Attempt: 1, MaxAttempts: 10}, boom)
	if got := buf.String(); !strings.Contains(got, `"level":"WARN"`) || strings.Contains(got, `"level":"ERROR"`) {
		t.Errorf("first attempt logged %s, want a warning", got)
	}
	buf.Reset()
	h.HandleError(t.Context(), &rivertype.JobRow{ID: 7, Kind: "create-balance-invoice", Attempt: 10, MaxAttempts: 10}, boom)
	if got := buf.String(); !strings.Contains(got, `"level":"ERROR"`) || !strings.Contains(got, "provider unreachable") || !strings.Contains(got, `"job_id":7`) {
		t.Errorf("last attempt logged %s, want an error naming the job and its failure", got)
	}
	buf.Reset()
	h.HandlePanic(t.Context(), &rivertype.JobRow{ID: 8, Kind: "forfeit-unpaid", Attempt: 1, MaxAttempts: 1}, "nil map", "goroutine 1")
	if got := buf.String(); !strings.Contains(got, `"level":"ERROR"`) || !strings.Contains(got, "nil map") {
		t.Errorf("panic logged %s, want an error", got)
	}
}

// Every job runs with a correlation id and in a span that records failure.
func TestObserve_CorrelatesAndTraces(t *testing.T) {
	spans := tracetest.NewSpanRecorder()
	o := &observe{tracer: sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans)).Tracer("test")}
	var correlation string

	err := o.Work(t.Context(), &rivertype.JobRow{ID: 42, Kind: "expire-unpaid-dp", Attempt: 1}, func(ctx context.Context) error {
		correlation = log.CorrelationID(ctx)
		return errors.New("boom")
	})

	if err == nil || correlation != "job-42" {
		t.Errorf("Work() = %v with correlation id %q, want the job's error and job-42", err, correlation)
	}
	ended := spans.Ended()
	if len(ended) != 1 || ended[0].Name() != "job expire-unpaid-dp" || ended[0].Status().Description != "job failed" {
		t.Errorf("spans = %+v, want one failed span for the job", ended)
	}
}
