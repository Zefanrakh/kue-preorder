package whatsapp_test

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Zefanrakh/kue-preorder/internal/platform/whatsapp"
)

// fakeGraph stands in for the Graph API: it records the request and answers
// with status and body.
func fakeGraph(t *testing.T, status int, body string, got *map[string]any, header *http.Header) *whatsapp.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v23.0/1234567890/messages" {
			t.Errorf("request %s %s, want POST /v23.0/1234567890/messages", r.Method, r.URL.Path)
		}
		if header != nil {
			*header = r.Header.Clone()
		}
		raw, _ := io.ReadAll(r.Body)
		if got != nil {
			_ = json.Unmarshal(raw, got)
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return whatsapp.New(whatsapp.Config{Token: "secret-token", PhoneNumberID: "1234567890", BaseURL: srv.URL + "/v23.0"}, srv.Client())
}

func TestSendAuthCode(t *testing.T) {
	var got map[string]any
	var header http.Header
	c := fakeGraph(t, http.StatusOK, `{"messaging_product":"whatsapp","messages":[{"id":"wamid.ABC"}]}`, &got, &header)

	id, err := c.SendAuthCode(t.Context(), "+6281234567890", "kode_masuk", "123456")

	if err != nil || id != "wamid.ABC" {
		t.Fatalf("SendAuthCode() = %q, %v", id, err)
	}
	if header.Get("Authorization") != "Bearer secret-token" || header.Get("Content-Type") != "application/json" {
		t.Errorf("headers = %v", header)
	}
	raw, _ := json.Marshal(got)
	want := `{"messaging_product":"whatsapp","recipient_type":"individual","template":{"components":[{"parameters":[{"text":"123456","type":"text"}],"type":"body"},{"index":"0","parameters":[{"text":"123456","type":"text"}],"sub_type":"url","type":"button"}],"language":{"code":"id"},"name":"kode_masuk"},"to":"6281234567890","type":"template"}`
	if string(raw) != want {
		t.Errorf("body =\n%s\nwant\n%s", raw, want)
	}
}

func TestSendAuthCode_Errors(t *testing.T) {
	tests := []struct {
		name          string
		status        int
		body          string
		temporary     bool
		undeliverable bool
	}{
		{"not on WhatsApp", 400, `{"error":{"message":"Message undeliverable","type":"OAuthException","code":131026,"fbtrace_id":"T1"}}`, false, true},
		{"throttled", 400, `{"error":{"message":"Rate limit hit","code":130429}}`, true, false},
		{"expired token", 401, `{"error":{"message":"Error validating access token","type":"OAuthException","code":190}}`, false, false},
		{"Meta is down", 503, `upstream unavailable`, true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := fakeGraph(t, tt.status, tt.body, nil, nil)
			_, err := c.SendAuthCode(t.Context(), "+6281234567890", "kode_masuk", "123456")
			var apiErr *whatsapp.APIError
			if !errors.As(err, &apiErr) || apiErr.Temporary() != tt.temporary || apiErr.Undeliverable() != tt.undeliverable {
				t.Errorf("error = %v, want temporary %t, undeliverable %t", err, tt.temporary, tt.undeliverable)
			}
			if strings.Contains(err.Error(), "secret-token") {
				t.Errorf("error %q leaks the token", err)
			}
		})
	}
}

// An unreachable API is a temporary failure, reported within the timeout.
func TestSendAuthCode_Unreachable(t *testing.T) {
	c := whatsapp.New(whatsapp.Config{Token: "secret-token", PhoneNumberID: "1", BaseURL: "http://127.0.0.1:1"}, &http.Client{Timeout: time.Second})

	_, err := c.SendAuthCode(t.Context(), "+6281234567890", "kode_masuk", "123456")

	var apiErr *whatsapp.APIError
	if !errors.As(err, &apiErr) || !apiErr.Temporary() || strings.Contains(err.Error(), "secret-token") {
		t.Errorf("error = %v, want a temporary failure without the token", err)
	}
}
