package telemetry

import (
	"context"
	"log/slog"
	"runtime"
	"slices"
	"strconv"
	"strings"

	"github.com/getsentry/sentry-go"
	"go.opentelemetry.io/otel/trace"

	"github.com/Zefanrakh/kue-preorder/internal/platform/log"
)

// Logger returns base extended to report every ERROR record to Sentry, so
// "log it at error level" is also "alert on it". Without Sentry it returns
// base unchanged.
func (t *Telemetry) Logger(base *slog.Logger) *slog.Logger {
	if t.sentry == nil {
		return base
	}
	return slog.New(&sentryHandler{next: base.Handler(), hub: t.sentry})
}

// sentryHandler passes every record on to next and also reports ERROR records.
type sentryHandler struct {
	next   slog.Handler
	hub    *sentry.Hub
	attrs  []slog.Attr // from With, already qualified by their groups
	groups []string
}

func (h *sentryHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return level >= slog.LevelError || h.next.Enabled(ctx, level)
}

func (h *sentryHandler) Handle(ctx context.Context, r slog.Record) error {
	var err error
	if h.next.Enabled(ctx, r.Level) {
		err = h.next.Handle(ctx, r)
	}
	if r.Level >= slog.LevelError {
		h.report(ctx, r)
	}
	return err
}

func (h *sentryHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	qualified := slices.Clone(h.attrs)
	for _, a := range attrs {
		qualified = append(qualified, slog.Attr{Key: h.qualify(a.Key), Value: a.Value})
	}
	return &sentryHandler{next: h.next.WithAttrs(attrs), hub: h.hub, attrs: qualified, groups: h.groups}
}

func (h *sentryHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	return &sentryHandler{next: h.next.WithGroup(name), hub: h.hub, attrs: h.attrs, groups: append(slices.Clone(h.groups), name)}
}

func (h *sentryHandler) qualify(key string) string {
	return strings.Join(append(slices.Clone(h.groups), key), ".")
}

func (h *sentryHandler) report(ctx context.Context, r slog.Record) {
	fields := map[string]any{}
	var cause error
	collect := func(a slog.Attr) {
		flatten(fields, a.Key, a.Value.Resolve())
		if err, ok := a.Value.Resolve().Any().(error); ok && cause == nil {
			cause = err
		}
	}
	for _, a := range h.attrs {
		collect(a)
	}
	r.Attrs(func(a slog.Attr) bool {
		collect(slog.Attr{Key: h.qualify(a.Key), Value: a.Value})
		return true
	})

	source := callSite(r.PC)
	event := sentry.NewEvent()
	event.Level = sentry.LevelError
	if r.Level > slog.LevelError {
		event.Level = sentry.LevelFatal
	}
	event.Logger = "slog"
	event.Message = r.Message
	event.Contexts = map[string]sentry.Context{"log": fields}
	// Group by call site, not by the error text, which carries ids that vary.
	event.Fingerprint = []string{"slog", r.Message, source}
	event.Tags = map[string]string{"source": source}
	if id := log.CorrelationID(ctx); id != "" {
		event.Tags[log.CorrelationIDKey] = id
	}
	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
		event.Tags[log.TraceIDKey] = sc.TraceID().String()
	}
	if cause != nil {
		event.Exception = []sentry.Exception{{Type: r.Message, Value: cause.Error(), Stacktrace: callerStack()}}
	}
	h.hub.CaptureEvent(event)
}

// flatten stores v under key, spreading groups into dotted keys.
func flatten(dst map[string]any, key string, v slog.Value) {
	if v.Kind() == slog.KindGroup {
		for _, a := range v.Group() {
			flatten(dst, key+"."+a.Key, a.Value.Resolve())
		}
		return
	}
	dst[key] = v.String()
}

func callSite(pc uintptr) string {
	if pc == 0 {
		return ""
	}
	frame, _ := runtime.CallersFrames([]uintptr{pc}).Next()
	return frame.Function + " (" + frame.File + ":" + strconv.Itoa(frame.Line) + ")"
}

// callerStack is the stack of the code that logged, without the logging
// machinery above it.
func callerStack() *sentry.Stacktrace {
	st := sentry.NewStacktrace()
	if st == nil {
		return nil
	}
	st.Frames = slices.DeleteFunc(st.Frames, func(f sentry.Frame) bool {
		if f.Module == "log/slog" {
			return true
		}
		ownFrame := strings.HasPrefix(f.Function, "(*sentryHandler).") || f.Function == "callerStack"
		return ownFrame && strings.HasSuffix(f.Module, "internal/platform/telemetry")
	})
	return st
}
