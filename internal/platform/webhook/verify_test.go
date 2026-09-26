package webhook_test

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/Zefanrakh/kue-preorder/internal/platform/webhook"
)

var (
	key    = []byte("0123456789abcdef0123456789abcdef")
	secret = "v1,whsec_" + base64.StdEncoding.EncodeToString(key)
	now    = time.Date(2026, 10, 5, 3, 0, 0, 0, time.UTC)
	body   = []byte(`{"user":{"phone":"6281234567890"},"sms":{"otp":"123456"}}`)
)

// sign builds the headers a sender holding k would send.
func sign(k []byte, id string, at time.Time, payload []byte) http.Header {
	ts := strconv.FormatInt(at.Unix(), 10)
	mac := hmac.New(sha256.New, k)
	mac.Write([]byte(id + "." + ts + "." + string(payload)))
	h := http.Header{}
	h.Set("webhook-id", id)
	h.Set("webhook-timestamp", ts)
	h.Set("webhook-signature", "v1,"+base64.StdEncoding.EncodeToString(mac.Sum(nil)))
	return h
}

func TestStandardVerifier(t *testing.T) {
	v, err := webhook.NewStandardVerifier(secret)
	if err != nil {
		t.Fatal(err)
	}

	id, err := v.Verify(sign(key, "msg_1", now, body), body, now)
	if err != nil || id != "msg_1" {
		t.Fatalf("Verify(valid) = %q, %v; want msg_1", id, err)
	}

	rotated := sign(key, "msg_1", now, body)
	rotated.Set("webhook-signature", "v1,b2xkIHNpZ25hdHVyZQ== "+rotated.Get("webhook-signature"))
	if _, err := v.Verify(rotated, body, now); err != nil {
		t.Errorf("Verify(two signatures, one good) error = %v, want accepted", err)
	}

	tests := map[string]struct {
		h    http.Header
		body []byte
	}{
		"another key":       {sign([]byte("another key, 16+ bytes long"), "msg_1", now, body), body},
		"tampered body":     {sign(key, "msg_1", now, body), []byte(`{"sms":{"otp":"999999"}}`)},
		"tampered id":       {func() http.Header { h := sign(key, "msg_1", now, body); h.Set("webhook-id", "msg_2"); return h }(), body},
		"too old":           {sign(key, "msg_1", now.Add(-6*time.Minute), body), body},
		"too far in future": {sign(key, "msg_1", now.Add(6*time.Minute), body), body},
		"no headers":        {http.Header{}, body},
		"signature version2": {func() http.Header {
			h := sign(key, "msg_1", now, body)
			h.Set("webhook-signature", "v2,"+h.Get("webhook-signature")[3:])
			return h
		}(), body},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := v.Verify(tt.h, tt.body, now); !errors.Is(err, webhook.ErrUnverified) {
				t.Errorf("Verify() error = %v, want ErrUnverified", err)
			}
		})
	}
}

func TestNewStandardVerifier_RejectsBadSecrets(t *testing.T) {
	for _, s := range []string{"", "whsec_not-base64!", "v1,whsec_" + base64.StdEncoding.EncodeToString([]byte("short")), "plain-secret"} {
		if _, err := webhook.NewStandardVerifier(s); err == nil {
			t.Errorf("NewStandardVerifier(%q) succeeded, want an error", s)
		}
	}
	if _, err := webhook.NewStandardVerifier("whsec_" + base64.StdEncoding.EncodeToString(key)); err != nil {
		t.Errorf("NewStandardVerifier(without v1,) error = %v", err)
	}
}
