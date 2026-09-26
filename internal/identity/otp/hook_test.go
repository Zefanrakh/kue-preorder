package otp_test

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Zefanrakh/kue-preorder/internal/identity/otp"
	"github.com/Zefanrakh/kue-preorder/internal/platform/clock"
	"github.com/Zefanrakh/kue-preorder/internal/platform/ratelimit"
	"github.com/Zefanrakh/kue-preorder/internal/platform/webhook"
	"github.com/Zefanrakh/kue-preorder/internal/platform/whatsapp"
)

var (
	key    = []byte("0123456789abcdef0123456789abcdef")
	secret = "v1,whsec_" + base64.StdEncoding.EncodeToString(key)
	now    = time.Date(2026, 10, 5, 3, 0, 0, 0, time.UTC)
)

// events keeps claimed deliveries in memory.
type events struct {
	mu      sync.Mutex
	claimed map[string]bool // id -> done
}

func (e *events) Claim(_ context.Context, _, id string, _ time.Time) (bool, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, ok := e.claimed[id]; ok {
		return false, nil
	}
	e.claimed[id] = false
	return true, nil
}

func (e *events) Done(_ context.Context, _, id string, _ time.Time) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.claimed[id] = true
	return nil
}

func (e *events) Release(_ context.Context, _, id string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.claimed[id] {
		delete(e.claimed, id)
	}
	return nil
}

// sender records codes, or fails with err.
type sender struct {
	mu   sync.Mutex
	sent []string // "phone code"
	err  error
}

func (s *sender) SendCode(_ context.Context, phone, code string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	s.sent = append(s.sent, phone+" "+code)
	return nil
}

type fixture struct {
	hook   *otp.Hook
	sender *sender
	events *events
	clock  *clock.Fake
	logs   *bytes.Buffer
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	v, err := webhook.NewStandardVerifier(secret)
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{sender: &sender{}, events: &events{claimed: map[string]bool{}}, clock: clock.NewFake(now), logs: &bytes.Buffer{}}
	limiter := ratelimit.New(f.clock, map[string]ratelimit.Rule{otp.Provider: otp.PerPhone})
	f.hook = otp.NewHook(v, f.events, f.sender, limiter, f.clock, slog.New(slog.NewJSONHandler(f.logs, nil)))
	return f
}

// deliver posts a signed delivery, as Supabase would.
func (f *fixture) deliver(t *testing.T, id, phone, code string) (int, string) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"user": map[string]any{"id": "b5c1", "phone": phone}, "sms": map[string]any{"otp": code}})
	ts := strconv.FormatInt(f.clock.Now().Unix(), 10)
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(id + "." + ts + "." + string(body)))
	r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/hooks/supabase/send-sms", bytes.NewReader(body))
	r.Header.Set("webhook-id", id)
	r.Header.Set("webhook-timestamp", ts)
	r.Header.Set("webhook-signature", "v1,"+base64.StdEncoding.EncodeToString(mac.Sum(nil)))
	w := httptest.NewRecorder()
	f.hook.ServeHTTP(w, r)
	return w.Code, w.Body.String()
}

func TestHook_SendsTheCode(t *testing.T) {
	f := newFixture(t)

	status, body := f.deliver(t, "msg_1", "6281234567890", "123456")

	if status != http.StatusOK || body != "{}" || len(f.sender.sent) != 1 || f.sender.sent[0] != "+6281234567890 123456" {
		t.Errorf("= %d %s, sent %v; want 200 and the code sent to +6281234567890", status, body, f.sender.sent)
	}
	if strings.Contains(f.logs.String(), "123456") {
		t.Errorf("logs hold the code: %s", f.logs.String())
	}
}

func TestHook_RetryIsSentOnce(t *testing.T) {
	f := newFixture(t)
	f.deliver(t, "msg_1", "6281234567890", "123456")

	status, _ := f.deliver(t, "msg_1", "6281234567890", "123456")

	if status != http.StatusOK || len(f.sender.sent) != 1 {
		t.Errorf("retry = %d, sent %d codes; want 200 and one code", status, len(f.sender.sent))
	}
}

func TestHook_RefusesUnsignedDeliveries(t *testing.T) {
	f := newFixture(t)
	r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/hooks/supabase/send-sms",
		strings.NewReader(`{"user":{"phone":"6281234567890"},"sms":{"otp":"123456"}}`))
	r.Header.Set("webhook-id", "msg_1")
	r.Header.Set("webhook-timestamp", strconv.FormatInt(now.Unix(), 10))
	r.Header.Set("webhook-signature", "v1,Zm9yZ2Vk")
	w := httptest.NewRecorder()

	f.hook.ServeHTTP(w, r)

	if w.Code != http.StatusUnauthorized || len(f.sender.sent) != 0 {
		t.Errorf("= %d, sent %v; want 401 and nothing sent", w.Code, f.sender.sent)
	}
}

func TestHook_RejectsBadDeliveries(t *testing.T) {
	tests := map[string]struct{ phone, code string }{
		"no phone":        {"", "123456"},
		"not a phone":     {"abc", "123456"},
		"short code":      {"6281234567890", "1234"},
		"letters in code": {"6281234567890", "12a456"},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			if status, _ := f.deliver(t, "msg_1", tt.phone, tt.code); status != http.StatusBadRequest || len(f.sender.sent) != 0 {
				t.Errorf("= %d, sent %v; want 400 and nothing sent", status, f.sender.sent)
			}
		})
	}
}

// Five codes an hour per number: the sixth waits, other numbers do not.
func TestHook_LimitsCodesPerNumber(t *testing.T) {
	f := newFixture(t)
	for i := range 5 {
		if status, _ := f.deliver(t, "msg_"+strconv.Itoa(i), "6281234567890", "123456"); status != http.StatusOK {
			t.Fatalf("code %d = %d, want 200", i+1, status)
		}
	}

	status, body := f.deliver(t, "msg_6", "6281234567890", "123456")
	if status != http.StatusTooManyRequests || !strings.Contains(body, "Terlalu sering") {
		t.Errorf("6th code = %d %s, want 429 with a message", status, body)
	}
	if status, _ := f.deliver(t, "msg_7", "6289876543210", "654321"); status != http.StatusOK {
		t.Errorf("another number = %d, want 200", status)
	}
	f.clock.Advance(12 * time.Minute)
	if status, _ := f.deliver(t, "msg_8", "6281234567890", "123456"); status != http.StatusOK {
		t.Errorf("after 12 minutes = %d, want one more code", status)
	}
}

func TestHook_SendFailures(t *testing.T) {
	tests := []struct {
		name    string
		err     error
		status  int
		message string
		level   string
	}{
		{"not on WhatsApp", &whatsapp.APIError{Status: 400, Code: 131026}, http.StatusBadRequest, "tidak bisa menerima pesan WhatsApp", "INFO"},
		{"WhatsApp down", &whatsapp.APIError{Status: 503}, http.StatusServiceUnavailable, "Coba lagi sebentar", "WARN"},
		{"expired token", &whatsapp.APIError{Status: 401, Code: 190}, http.StatusInternalServerError, "Coba lagi nanti", "ERROR"},
		{"anything else", errors.New("boom"), http.StatusInternalServerError, "Coba lagi nanti", "ERROR"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)
			f.sender.err = tt.err

			status, body := f.deliver(t, "msg_1", "6281234567890", "123456")

			if status != tt.status || !strings.Contains(body, tt.message) || !strings.Contains(body, `"http_code":`+strconv.Itoa(tt.status)) {
				t.Errorf("= %d %s, want %d mentioning %q", status, body, tt.status, tt.message)
			}
			if !strings.Contains(f.logs.String(), `"level":"`+tt.level+`"`) || strings.Contains(f.logs.String(), "123456") || strings.Contains(f.logs.String(), "6281234567890") {
				t.Errorf("logs = %s, want %s without the code or the full number", f.logs.String(), tt.level)
			}

			// The failed delivery was released: Supabase's retry sends it.
			f.sender.err = nil
			if status, _ := f.deliver(t, "msg_1", "6281234567890", "123456"); status != http.StatusOK || len(f.sender.sent) != 1 {
				t.Errorf("retry = %d, sent %v; want the code sent now", status, f.sender.sent)
			}
		})
	}
}
