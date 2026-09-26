// Package otp delivers the sign-in codes of Supabase Auth over WhatsApp
// (docs/architecture.md §8). Supabase makes the code and calls Hook, its
// Send SMS hook, over HTTPS; Hook checks the signature and sends the code
// with an approved authentication template. The code is never stored.
package otp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"time"

	"github.com/Zefanrakh/kue-preorder/internal/identity"
	"github.com/Zefanrakh/kue-preorder/internal/platform/clock"
	"github.com/Zefanrakh/kue-preorder/internal/platform/ratelimit"
	"github.com/Zefanrakh/kue-preorder/internal/platform/webhook"
	"github.com/Zefanrakh/kue-preorder/internal/platform/whatsapp"
)

// Provider names these deliveries in webhook_events.
const Provider = "supabase_auth"

// PerPhone is the most codes one number gets (§27): five an hour. On top,
// Supabase waits 60 seconds between two codes for the same number. Every code
// costs a WhatsApp message, so this keeps a bot from spending the balance.
var PerPhone = ratelimit.Rule{Every: 12 * time.Minute, Burst: 5}

// maxBody bounds a hook delivery; Supabase sends well under a kilobyte.
const maxBody = 64 << 10

var sixDigits = regexp.MustCompile(`^[0-9]{6}$`)

// Sender delivers a sign-in code to a phone number in E.164.
type Sender interface {
	SendCode(ctx context.Context, phone, code string) error
}

// Events remembers deliveries so a retried one sends once; webhook.Events
// implements it.
type Events interface {
	Claim(ctx context.Context, provider, id string, at time.Time) (bool, error)
	Done(ctx context.Context, provider, id string, at time.Time) error
	Release(ctx context.Context, provider, id string) error
}

// Hook is Supabase Auth's Send SMS hook: POST /hooks/supabase/send-sms.
type Hook struct {
	verifier *webhook.StandardVerifier
	events   Events
	sender   Sender
	perPhone *ratelimit.Limiter
	clock    clock.Clock
	logger   *slog.Logger
}

// NewHook returns the hook. perPhone limits codes per number (PerPhone).
func NewHook(v *webhook.StandardVerifier, events Events, sender Sender, perPhone *ratelimit.Limiter, clk clock.Clock, logger *slog.Logger) *Hook {
	return &Hook{verifier: v, events: events, sender: sender, perPhone: perPhone, clock: clk, logger: logger}
}

type delivery struct {
	User struct {
		Phone string `json:"phone"`
	} `json:"user"`
	SMS struct {
		OTP string `json:"otp"`
	} `json:"sms"`
}

// ServeHTTP implements http.Handler. It answers within Supabase's five
// seconds: 200 when the code is on its way (or was already), and otherwise
// an error Supabase shows the person signing in. Supabase retries 429 and
// 503; the webhook id keeps a retry from sending twice.
func (h *Hook) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	now := h.clock.Now()
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		reply(w, http.StatusBadRequest, "Permintaan tidak valid.")
		return
	}
	id, err := h.verifier.Verify(r.Header, body, now)
	if err != nil {
		h.logger.WarnContext(ctx, "send-sms hook refused", slog.Any("error", err))
		reply(w, http.StatusUnauthorized, "Permintaan tidak dikenali.")
		return
	}
	var d delivery
	phone := ""
	if json.Unmarshal(body, &d) == nil {
		phone = identity.NormalizePhone(d.User.Phone)
	}
	if phone == "" || !sixDigits.MatchString(d.SMS.OTP) {
		h.logger.WarnContext(ctx, "send-sms hook without a phone number or a six-digit code", slog.String("webhook_id", id))
		reply(w, http.StatusBadRequest, "Nomor HP tidak valid.")
		return
	}
	if !h.perPhone.Allow(Provider, phone) {
		h.logger.InfoContext(ctx, "sign-in codes limited", slog.String("phone", mask(phone)))
		reply(w, http.StatusTooManyRequests, "Terlalu sering meminta kode. Coba lagi dalam satu jam.")
		return
	}

	fresh, err := h.events.Claim(ctx, Provider, id, now)
	if err != nil {
		h.logger.ErrorContext(ctx, "claim send-sms delivery", slog.Any("error", err))
		reply(w, http.StatusServiceUnavailable, "Kode belum bisa dikirim. Coba lagi sebentar.")
		return
	}
	if !fresh {
		reply(w, http.StatusOK, "")
		return
	}
	if err := h.sender.SendCode(ctx, phone, d.SMS.OTP); err != nil {
		h.failed(ctx, w, id, phone, err)
		return
	}
	if err := h.events.Done(ctx, Provider, id, h.clock.Now()); err != nil {
		h.logger.WarnContext(ctx, "mark send-sms delivery done", slog.Any("error", err))
	}
	reply(w, http.StatusOK, "")
}

// failed releases the delivery so a retry can send, and tells Supabase what
// to show: a number without WhatsApp is the customer's to fix; an outage is
// worth a retry; anything else is ours, and alerts.
func (h *Hook) failed(ctx context.Context, w http.ResponseWriter, id, phone string, err error) {
	if rerr := h.events.Release(ctx, Provider, id); rerr != nil {
		h.logger.WarnContext(ctx, "release send-sms delivery", slog.Any("error", rerr))
	}
	var api *whatsapp.APIError
	switch {
	case errors.As(err, &api) && api.Undeliverable():
		h.logger.InfoContext(ctx, "sign-in code undeliverable", slog.String("phone", mask(phone)), slog.Any("error", err))
		reply(w, http.StatusBadRequest, "Nomor ini tidak bisa menerima pesan WhatsApp. Pastikan nomornya terdaftar di WhatsApp.")
	case errors.As(err, &api) && api.Temporary():
		h.logger.WarnContext(ctx, "sign-in code not sent yet", slog.String("phone", mask(phone)), slog.Any("error", err))
		reply(w, http.StatusServiceUnavailable, "Kode belum bisa dikirim. Coba lagi sebentar.")
	default:
		h.logger.ErrorContext(ctx, "send sign-in code", slog.String("phone", mask(phone)), slog.Any("error", err))
		reply(w, http.StatusInternalServerError, "Kode belum bisa dikirim. Coba lagi nanti.")
	}
}

// reply answers in the shape Supabase Auth hooks read: an empty object on
// success, {"error": {"http_code", "message"}} otherwise.
func reply(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if status == http.StatusOK {
		_, _ = w.Write([]byte("{}"))
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"http_code": status, "message": message}})
}

// mask keeps a phone number out of logs but for its country, operator
// prefix, and last digits: enough to follow up, not enough to call.
func mask(phone string) string {
	if len(phone) < 10 {
		return "****"
	}
	return phone[:6] + "****" + phone[len(phone)-3:]
}
