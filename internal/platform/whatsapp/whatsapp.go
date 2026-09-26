// Package whatsapp sends messages through the WhatsApp Cloud API (Meta
// Graph API): sign-in codes now (§8), notifications and purchase orders
// later (§14, §19). Messages that start a conversation must use templates
// Meta has approved.
package whatsapp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"
)

// DefaultBaseURL is the Graph API version this client speaks. Meta retires
// versions about two years after release; check it when setting up the
// account and move it forward with a test run.
const DefaultBaseURL = "https://graph.facebook.com/v23.0"

// Timeout bounds one call. A sign-in code must be sent within the five
// seconds Supabase gives its hook, so this leaves room for the rest.
const Timeout = 3500 * time.Millisecond

// Config says which WhatsApp number sends and with which credentials.
type Config struct {
	Token         string // a system user's permanent access token
	PhoneNumberID string // the sending number's id, not the number itself
	BaseURL       string // empty: DefaultBaseURL
}

// Client sends messages. It is safe for concurrent use.
type Client struct {
	cfg  Config
	http *http.Client
}

// New returns a Client. hc may be nil for a client with Timeout.
func New(cfg Config, hc *http.Client) *Client {
	if cfg.BaseURL == "" {
		cfg.BaseURL = DefaultBaseURL
	}
	if hc == nil {
		hc = &http.Client{Timeout: Timeout}
	}
	return &Client{cfg: cfg, http: hc}
}

// Parameter fills one placeholder of a template.
type Parameter struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// Component is one part of a template message: its body or a button.
type Component struct {
	Type       string      `json:"type"`
	SubType    string      `json:"sub_type,omitempty"`
	Index      string      `json:"index,omitempty"`
	Parameters []Parameter `json:"parameters"`
}

// APIError is an error the Cloud API returned.
type APIError struct {
	Status  int
	Code    int
	Subcode int
	Type    string
	Message string
	TraceID string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("whatsapp: HTTP %d, error %d/%d %s: %s (trace %s)", e.Status, e.Code, e.Subcode, e.Type, e.Message, e.TraceID)
}

// rateLimitCodes are Cloud API errors that pass with time.
var rateLimitCodes = []int{4, 80007, 130429, 131048, 131056}

// Temporary reports whether sending again later may work: the API was down
// or throttled.
func (e *APIError) Temporary() bool {
	return e.Status >= 500 || e.Status == http.StatusTooManyRequests || slices.Contains(rateLimitCodes, e.Code)
}

// Undeliverable reports whether the recipient cannot get the message, such
// as a number without WhatsApp.
func (e *APIError) Undeliverable() bool { return e.Code == 131026 }

// SendTemplate sends an approved template to a phone number in E.164 and
// returns the message id.
func (c *Client) SendTemplate(ctx context.Context, to, name, language string, components []Component) (string, error) {
	payload, err := json.Marshal(map[string]any{
		"messaging_product": "whatsapp",
		"recipient_type":    "individual",
		"to":                strings.TrimPrefix(to, "+"),
		"type":              "template",
		"template": map[string]any{
			"name":       name,
			"language":   map[string]string{"code": language},
			"components": components,
		},
	})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.BaseURL+"/"+c.cfg.PhoneNumberID+"/messages", bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+c.cfg.Token)
	req.Header.Set("Content-Type", "application/json")

	res, err := c.http.Do(req)
	if err != nil {
		// The URL in err holds the number's id, never the token.
		return "", &APIError{Status: http.StatusServiceUnavailable, Message: "request failed: " + err.Error()}
	}
	defer func() { _ = res.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(res.Body, 64<<10))
	if err != nil {
		return "", &APIError{Status: http.StatusServiceUnavailable, Message: "read response: " + err.Error()}
	}
	if res.StatusCode/100 != 2 {
		var e struct {
			Error struct {
				Message   string `json:"message"`
				Type      string `json:"type"`
				Code      int    `json:"code"`
				Subcode   int    `json:"error_subcode"`
				FBTraceID string `json:"fbtrace_id"`
			} `json:"error"`
		}
		_ = json.Unmarshal(body, &e)
		return "", &APIError{Status: res.StatusCode, Code: e.Error.Code, Subcode: e.Error.Subcode, Type: e.Error.Type, Message: e.Error.Message, TraceID: e.Error.FBTraceID}
	}
	var ok struct {
		Messages []struct {
			ID string `json:"id"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &ok); err != nil || len(ok.Messages) == 0 {
		return "", fmt.Errorf("whatsapp: unexpected response %q", body)
	}
	return ok.Messages[0].ID, nil
}

// SendAuthCode sends a sign-in code with an authentication template, whose
// text Meta fixes; the code fills its body and its copy-code button.
func (c *Client) SendAuthCode(ctx context.Context, to, template, code string) (string, error) {
	param := []Parameter{{Type: "text", Text: code}}
	return c.SendTemplate(ctx, to, template, "id", []Component{
		{Type: "body", Parameters: param},
		{Type: "button", SubType: "url", Index: "0", Parameters: param},
	})
}
