package httpserver_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Zefanrakh/kue-preorder/internal/platform/httpserver"
)

func TestClientIP(t *testing.T) {
	tests := []struct {
		name    string
		header  string
		remote  string
		headers map[string][]string
		want    string
	}{
		{"connection address without a proxy", "", "203.0.113.7:51000", nil, "203.0.113.7"},
		{"proxy header", "CF-Connecting-IP", "10.0.0.1:443", map[string][]string{"Cf-Connecting-Ip": {"198.51.100.9"}}, "198.51.100.9"},
		{"a spoofed header is ignored without a proxy", "", "203.0.113.7:51000", map[string][]string{"Cf-Connecting-Ip": {"1.1.1.1"}}, "203.0.113.7"},
		{"last X-Forwarded-For entry", "X-Forwarded-For", "10.0.0.1:443", map[string][]string{"X-Forwarded-For": {"1.1.1.1, 198.51.100.9"}}, "198.51.100.9"},
		{"request that bypassed the proxy", "CF-Connecting-IP", "203.0.113.7:51000", nil, "203.0.113.7"},
		{"garbage in the header", "CF-Connecting-IP", "203.0.113.7:51000", map[string][]string{"Cf-Connecting-Ip": {"not an ip"}}, "203.0.113.7"},
		{"IPv6 counts per /64", "CF-Connecting-IP", "10.0.0.1:443", map[string][]string{"Cf-Connecting-Ip": {"2001:db8:1:2:aaaa::1"}}, "2001:db8:1:2::/64"},
		{"IPv4 in IPv6", "", "[::ffff:203.0.113.7]:51000", nil, "203.0.113.7"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got string
			h := httpserver.ClientIP(tt.header, http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				got = httpserver.ClientIPFrom(r.Context())
			}))
			r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/x", nil)
			r.RemoteAddr = tt.remote
			for k, v := range tt.headers {
				r.Header[k] = v
			}
			h.ServeHTTP(httptest.NewRecorder(), r)
			if got != tt.want {
				t.Errorf("ClientIP = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCacheControl(t *testing.T) {
	status := http.StatusOK
	h := httpserver.CacheControl(map[string]time.Duration{"/shop": time.Minute}, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
	}))
	tests := []struct {
		method, path string
		status       int
		want         string
	}{
		{http.MethodGet, "/shop", http.StatusOK, "public, max-age=60"},
		{http.MethodGet, "/shop", http.StatusNotFound, "no-store"},
		{http.MethodGet, "/shop", http.StatusInternalServerError, "no-store"},
		{http.MethodPost, "/shop", http.StatusOK, "no-store"},
		{http.MethodGet, "/orders", http.StatusOK, "no-store"},
	}
	for _, tt := range tests {
		status = tt.status
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequestWithContext(t.Context(), tt.method, tt.path, nil))
		if got := w.Header().Get("Cache-Control"); got != tt.want {
			t.Errorf("%s %s -> %d: Cache-Control = %q, want %q", tt.method, tt.path, tt.status, got, tt.want)
		}
	}
}

// A handler that writes a body without calling WriteHeader answers 200.
func TestCacheControl_ImplicitOK(t *testing.T) {
	h := httpserver.CacheControl(map[string]time.Duration{"/shop": time.Minute}, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/shop", nil))
	if got := w.Header().Get("Cache-Control"); got != "public, max-age=60" {
		t.Errorf("Cache-Control = %q, want public", got)
	}
}
