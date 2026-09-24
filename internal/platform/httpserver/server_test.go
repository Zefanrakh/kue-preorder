package httpserver_test

import (
	"context"
	"io"
	"net"
	"net/http"
	"testing"

	"github.com/Zefanrakh/kue-preorder/internal/platform/httpserver"
)

func listen(t *testing.T) net.Listener {
	t.Helper()
	var lc net.ListenConfig
	ln, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	return ln
}

func TestRun_ServesUntilContextCancelled(t *testing.T) {
	ln := listen(t)
	mux := http.NewServeMux()
	mux.Handle("GET /healthz", httpserver.Healthz())
	srv := httpserver.New(mux, discardLogger())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- httpserver.Run(ctx, srv, ln) }()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+ln.Addr().String()+"/healthz", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(body) != "ok" {
		t.Errorf("GET /healthz = %d %q, want 200 \"ok\"", resp.StatusCode, body)
	}

	cancel()
	if err := <-done; err != nil {
		t.Errorf("Run() error = %v, want nil after clean shutdown", err)
	}
}

func TestRun_ReturnsErrorWhenListenerFails(t *testing.T) {
	ln := listen(t)
	_ = ln.Close()

	err := httpserver.Run(t.Context(), httpserver.New(http.NotFoundHandler(), discardLogger()), ln)
	if err == nil {
		t.Fatal("Run() error = nil, want error for a closed listener")
	}
}
