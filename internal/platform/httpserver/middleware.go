package httpserver

import (
	"crypto/rand"
	"net/http"

	"github.com/Zefanrakh/kue-preorder/internal/platform/log"
)

// CorrelationIDHeader carries the correlation id in requests and responses.
const CorrelationIDHeader = "X-Request-ID"

const maxCorrelationIDLen = 128

// CorrelationID puts a correlation id in the request context and echoes it in
// the response. A well-formed incoming X-Request-ID (e.g. from the platform's
// proxy) is reused; anything else is replaced so clients cannot inject
// arbitrary text into logs.
func CorrelationID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(CorrelationIDHeader)
		if !validCorrelationID(id) {
			id = rand.Text()
		}
		w.Header().Set(CorrelationIDHeader, id)
		next.ServeHTTP(w, r.WithContext(log.WithCorrelationID(r.Context(), id)))
	})
}

func validCorrelationID(id string) bool {
	if id == "" || len(id) > maxCorrelationIDLen {
		return false
	}
	for _, c := range []byte(id) {
		switch {
		case 'a' <= c && c <= 'z', 'A' <= c && c <= 'Z', '0' <= c && c <= '9':
		case c == '-', c == '_', c == '.', c == ':':
		default:
			return false
		}
	}
	return true
}
