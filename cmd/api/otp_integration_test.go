//go:build integration

package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/Zefanrakh/kue-preorder/internal/platform/clock"
	"github.com/Zefanrakh/kue-preorder/internal/platform/config"
	"github.com/Zefanrakh/kue-preorder/internal/platform/db/dbtest"
	"github.com/Zefanrakh/kue-preorder/internal/platform/log"
)

// Wired as serve wires it in development: a signed delivery sends the code
// (to the log, without WhatsApp) once, however often Supabase retries it.
func TestSendSMSHook_EndToEnd(t *testing.T) {
	d := dbtest.New(t)
	logs := &syncBuffer{}
	cfg := config.Config{AppEnv: config.EnvDevelopment, SendSMSHookSecret: "v1,whsec_" + base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))}
	hook, err := sendSMSHook(cfg, d, log.New(logs, slog.LevelDebug))
	noErr(t, err)
	srv := httptest.NewServer(hook)
	t.Cleanup(srv.Close)

	body := `{"user":{"phone":"6281234567890"},"sms":{"otp":"482915"}}`
	deliver := func() int {
		ts := strconv.FormatInt(clock.Real{}.Now().Unix(), 10)
		mac := hmac.New(sha256.New, []byte("0123456789abcdef0123456789abcdef"))
		mac.Write([]byte("msg_e2e." + ts + "." + body))
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, srv.URL, strings.NewReader(body))
		noErr(t, err)
		req.Header.Set("webhook-id", "msg_e2e")
		req.Header.Set("webhook-timestamp", ts)
		req.Header.Set("webhook-signature", "v1,"+base64.StdEncoding.EncodeToString(mac.Sum(nil)))
		res, err := srv.Client().Do(req)
		noErr(t, err)
		_ = res.Body.Close()
		return res.StatusCode
	}

	if first, retry := deliver(), deliver(); first != http.StatusOK || retry != http.StatusOK {
		t.Fatalf("deliveries = %d, %d; want 200 twice", first, retry)
	}
	var sent int
	for _, rec := range logs.records(t) {
		if rec["code"] == "482915" {
			sent++
		}
	}
	var processed int
	noErr(t, d.Pool().QueryRow(t.Context(), "select count(*) from webhook_events where provider = 'supabase_auth' and event_id = 'msg_e2e' and processed_at is not null").Scan(&processed))
	if sent != 1 || processed != 1 {
		t.Errorf("code sent %d times, %d processed deliveries; want one of each", sent, processed)
	}
}

// Outside development, codes need WhatsApp.
func TestSendSMSHook_NeedsWhatsAppOutsideDevelopment(t *testing.T) {
	if _, err := sendSMSHook(config.Config{AppEnv: config.EnvStaging}, nil, slog.New(slog.DiscardHandler)); err == nil {
		t.Error("staging without WhatsApp started, want an error")
	}
}
