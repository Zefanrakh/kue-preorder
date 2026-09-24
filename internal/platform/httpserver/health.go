package httpserver

import (
	"context"
	"log/slog"
	"net/http"
	"time"
)

const readyzTimeout = 2 * time.Second

// Pinger reports whether a dependency is reachable.
type Pinger interface {
	Ping(ctx context.Context) error
}

// Healthz reports that the process is alive. It checks no dependencies, so a
// database outage does not make the platform restart healthy instances.
func Healthz() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeText(w, http.StatusOK, "ok")
	})
}

// Readyz reports whether the instance can serve traffic: the database must
// answer a ping within readyzTimeout.
func Readyz(db Pinger, logger *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), readyzTimeout)
		defer cancel()
		if err := db.Ping(ctx); err != nil {
			logger.WarnContext(r.Context(), "readiness check failed", slog.String("dependency", "database"), slog.Any("error", err))
			writeText(w, http.StatusServiceUnavailable, "database unavailable")
			return
		}
		writeText(w, http.StatusOK, "ok")
	})
}

func writeText(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}
