package jobs

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/Zefanrakh/kue-preorder/internal/platform/clock"
	"github.com/Zefanrakh/kue-preorder/internal/platform/log"
	"github.com/Zefanrakh/kue-preorder/internal/platform/outbox"
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

func TestMerge_KeepsEverySubscriber(t *testing.T) {
	a := func(outbox.Stored) (river.JobArgs, error) { return nil, nil }
	b := func(outbox.Stored) (river.JobArgs, error) { return nil, nil }

	got := Merge(map[string][]Subscriber{"order.confirmed": {a}}, map[string][]Subscriber{"order.confirmed": {b}, "order.cancelled": {b}})

	if len(got["order.confirmed"]) != 2 || len(got["order.cancelled"]) != 1 {
		t.Errorf("Merge() = %d and %d subscribers, want 2 and 1", len(got["order.confirmed"]), len(got["order.cancelled"]))
	}
}

// Daily jobs run at their time in Asia/Jakarta, whatever zone the clock is in.
func TestDailyAt(t *testing.T) {
	five := DailyAt(0, 5)
	wib := func(day, hour, minute int) time.Time {
		return time.Date(2026, time.October, day, hour, minute, 0, 0, clock.Jakarta)
	}
	for _, tt := range []struct {
		now, want time.Time
	}{
		{wib(6, 0, 4), wib(6, 0, 5)},
		{wib(6, 0, 5), wib(7, 0, 5)}, // just ran
		{wib(6, 18, 0), wib(7, 0, 5)},
		{wib(6, 18, 0).UTC(), wib(7, 0, 5)},
		{time.Date(2026, time.October, 5, 17, 3, 0, 0, time.UTC), wib(6, 0, 5)}, // 00.03 WIB on the 6th
	} {
		if got := five.Next(tt.now); !got.Equal(tt.want) {
			t.Errorf("Next(%v) = %v, want %v", tt.now, got, tt.want)
		}
	}
}
