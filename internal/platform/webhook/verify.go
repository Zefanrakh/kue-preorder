// Package webhook holds what every incoming webhook shares
// (docs/architecture.md §18): verifying the sender and remembering which
// events were already handled, so a retried delivery has one effect.
package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// ErrUnverified means a request does not prove it comes from the sender.
var ErrUnverified = errors.New("webhook signature not verified")

// tolerance is how far a signed timestamp may be from now, either way:
// older deliveries could be replays.
const tolerance = 5 * time.Minute

// StandardVerifier checks Standard Webhooks signatures
// (https://www.standardwebhooks.com), as Supabase Auth hooks and Resend send
// them: an HMAC-SHA256 over "id.timestamp.body" in the webhook-signature
// header, keyed with the shared secret.
type StandardVerifier struct {
	key []byte
}

// NewStandardVerifier reads a secret written as "v1,whsec_<base64>" (as
// Supabase shows it) or "whsec_<base64>".
func NewStandardVerifier(secret string) (*StandardVerifier, error) {
	s := strings.TrimPrefix(strings.TrimSpace(secret), "v1,")
	s, ok := strings.CutPrefix(s, "whsec_")
	if !ok {
		return nil, errors.New(`webhook secret must look like "v1,whsec_<base64>"`)
	}
	key, err := base64.StdEncoding.DecodeString(s)
	if err != nil || len(key) < 16 {
		return nil, errors.New("webhook secret is not base64 of at least 16 bytes")
	}
	return &StandardVerifier{key: key}, nil
}

// Verify checks the headers of a delivery against its body at now and
// returns the delivery's id, which stays the same across its retries.
func (v *StandardVerifier) Verify(h http.Header, body []byte, now time.Time) (string, error) {
	id, ts, sigs := h.Get("webhook-id"), h.Get("webhook-timestamp"), h.Get("webhook-signature")
	if id == "" || ts == "" || sigs == "" {
		return "", fmt.Errorf("%w: missing webhook-id, webhook-timestamp, or webhook-signature", ErrUnverified)
	}
	secs, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return "", fmt.Errorf("%w: webhook-timestamp %q is not a number", ErrUnverified, ts)
	}
	if d := now.Sub(time.Unix(secs, 0)); d > tolerance || d < -tolerance {
		return "", fmt.Errorf("%w: timestamp is %v away from now", ErrUnverified, d.Round(time.Second))
	}

	mac := hmac.New(sha256.New, v.key)
	mac.Write([]byte(id + "." + ts + "."))
	mac.Write(body)
	want := mac.Sum(nil)
	for _, sig := range strings.Fields(sigs) {
		version, encoded, ok := strings.Cut(sig, ",")
		if !ok || version != "v1" {
			continue
		}
		got, err := base64.StdEncoding.DecodeString(encoded)
		if err == nil && hmac.Equal(got, want) {
			return id, nil
		}
	}
	return "", fmt.Errorf("%w: no matching v1 signature", ErrUnverified)
}
