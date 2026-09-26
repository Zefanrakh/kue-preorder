package httpserver

import (
	"net/http"
	"strconv"
	"time"
)

// CacheControl marks every response as not to be stored, except successful
// GET responses at the paths in public, which shared caches such as a CDN
// may keep for the given time (§22). An error is never cached. Only public,
// identical-for-everyone data belongs in public; an order or a customer's
// data must never be cached.
func CacheControl(public map[string]time.Duration, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		cw := &cacheWriter{ResponseWriter: w}
		if maxAge, ok := public[r.URL.Path]; ok && r.Method == http.MethodGet {
			cw.public = "public, max-age=" + strconv.Itoa(int(maxAge.Seconds()))
		}
		next.ServeHTTP(cw, r)
	})
}

// cacheWriter makes a response public once it turns out to be a 200.
type cacheWriter struct {
	http.ResponseWriter
	public string
	wrote  bool
}

func (w *cacheWriter) WriteHeader(code int) {
	if !w.wrote {
		w.wrote = true
		if code == http.StatusOK && w.public != "" {
			w.Header().Set("Cache-Control", w.public)
		}
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *cacheWriter) Write(b []byte) (int, error) {
	if !w.wrote {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(b)
}

// Flush lets streaming responses through.
func (w *cacheWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		if !w.wrote {
			w.WriteHeader(http.StatusOK)
		}
		f.Flush()
	}
}

// Unwrap exposes the original writer to http.ResponseController.
func (w *cacheWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
