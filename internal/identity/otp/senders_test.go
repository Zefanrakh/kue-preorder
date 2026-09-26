package otp_test

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Zefanrakh/kue-preorder/internal/identity/otp"
	"github.com/Zefanrakh/kue-preorder/internal/platform/whatsapp"
)

func TestWhatsAppSender(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &got)
		_, _ = w.Write([]byte(`{"messages":[{"id":"wamid.1"}]}`))
	}))
	t.Cleanup(srv.Close)
	s := otp.WhatsAppSender{Client: whatsapp.New(whatsapp.Config{Token: "t", PhoneNumberID: "1", BaseURL: srv.URL}, srv.Client()), Template: "kode_masuk"}

	if err := s.SendCode(t.Context(), "+6281234567890", "123456"); err != nil {
		t.Fatal(err)
	}
	if got["to"] != "6281234567890" || got["template"].(map[string]any)["name"] != "kode_masuk" {
		t.Errorf("request = %v", got)
	}
}

func TestLogSender(t *testing.T) {
	var logs bytes.Buffer
	if err := (otp.LogSender{Logger: slog.New(slog.NewJSONHandler(&logs, nil))}).SendCode(t.Context(), "+6281234567890", "123456"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(logs.String(), "123456") || !strings.Contains(logs.String(), "development only") {
		t.Errorf("logs = %s, want the code, marked development only", logs.String())
	}
}
