package httpserver_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/Zefanrakh/kue-preorder/internal/platform/httpserver"
)

func TestRecover_PanicBecomes500AndIsLogged(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	h := httpserver.Recover(logger, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("nil map write in handler")
	}))

	rec := serve(t, h, "/boom")

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
	for _, want := range []string{`"level":"ERROR"`, "panic while serving request", "nil map write in handler", "/boom", "recover_test.go"} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("log misses %q; got %s", want, logs.String())
		}
	}
}

func TestRecover_LetsErrAbortHandlerThrough(t *testing.T) {
	h := httpserver.Recover(discardLogger(), http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic(http.ErrAbortHandler)
	}))

	defer func() {
		if v := recover(); v != http.ErrAbortHandler { //nolint:errorlint // net/http compares with ==
			t.Errorf("recovered %v, want http.ErrAbortHandler to pass through", v)
		}
	}()
	serve(t, h, "/abort")
}

func TestRecover_PassesNormalRequests(t *testing.T) {
	rec := serve(t, httpserver.Recover(discardLogger(), httpserver.Healthz()), "/healthz")

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
}

func TestConnectRecover_PanicBecomesInternalError(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	const procedure = "/test.v1.TestService/Explode"
	mux := http.NewServeMux()
	mux.Handle(procedure, connect.NewUnaryHandler(procedure,
		func(context.Context, *connect.Request[emptypb.Empty]) (*connect.Response[emptypb.Empty], error) {
			panic("secret detail from a bug")
		},
		httpserver.ConnectRecover(logger)))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	client := connect.NewClient[emptypb.Empty, emptypb.Empty](srv.Client(), srv.URL+procedure)

	_, err := client.CallUnary(t.Context(), connect.NewRequest(&emptypb.Empty{}))

	var connectErr *connect.Error
	if !errors.As(err, &connectErr) || connectErr.Code() != connect.CodeInternal {
		t.Fatalf("error = %v, want Internal", err)
	}
	if strings.Contains(err.Error(), "secret detail") {
		t.Errorf("client sees the panic value: %v", err)
	}
	for _, want := range []string{"panic in rpc handler", "secret detail from a bug", procedure} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("log misses %q; got %s", want, logs.String())
		}
	}
}
