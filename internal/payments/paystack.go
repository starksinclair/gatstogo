// Package payments is a minimal Paystack API client: just the two calls
// this app needs (Initialize a transaction, Verify one) plus webhook
// signature verification. It talks to Paystack's HTTP API directly with
// net/http rather than depending on an unofficial/unmaintained Go SDK.
//
// Paystack was chosen over Flutterwave for this integration: better
// documentation and reliability track record, HMAC-signed webhooks, and
// its bank_transfer/ussd/card channels map directly onto the payment
// options already present in the customer-facing buy form.
package payments

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha512"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

const defaultBaseURL = "https://api.paystack.co"

// Client is a Paystack API client scoped to one secret key.
type Client struct {
	SecretKey  string
	BaseURL    string // defaults to the real Paystack API; overridden in tests
	HTTPClient *http.Client
}

func NewClient(secretKey string) *Client {
	return &Client{
		SecretKey:  secretKey,
		HTTPClient: &http.Client{Timeout: 15 * time.Second},
	}
}

// InitializeParams are the fields this app sets on a
// transaction/initialize call.
type InitializeParams struct {
	Email       string
	AmountKobo  int64
	Reference   string
	CallbackURL string
	Channels    []string
}

// InitializeResult is what a caller needs back: where to send the
// customer to actually pay.
type InitializeResult struct {
	AuthorizationURL string `json:"authorization_url"`
	AccessCode       string `json:"access_code"`
	Reference        string `json:"reference"`
}

// Initialize starts a Paystack transaction and returns the URL to redirect
// the customer's browser to.
func (c *Client) Initialize(ctx context.Context, p InitializeParams) (*InitializeResult, error) {
	body, err := json.Marshal(map[string]any{
		"email":        p.Email,
		"amount":       p.AmountKobo,
		"reference":    p.Reference,
		"callback_url": p.CallbackURL,
		"channels":     p.Channels,
	})
	if err != nil {
		return nil, err
	}

	var out apiEnvelope[InitializeResult]
	if err := c.do(ctx, http.MethodPost, "/transaction/initialize", body, &out); err != nil {
		return nil, err
	}
	return &out.Data, nil
}

// VerifyResult is the subset of Paystack's verify response this app acts
// on.
type VerifyResult struct {
	Status     string `json:"status"` // "success", "failed", "abandoned", ...
	Reference  string `json:"reference"`
	AmountKobo int64  `json:"amount"`
	Channel    string `json:"channel"`
}

// Verify checks a transaction's actual status directly with Paystack. Used
// as the fallback confirmation path when the customer's browser lands back
// on the callback URL -- the webhook (VerifyWebhookSignature below) is the
// primary confirmation path, but the two are expected to race, which is
// why ticket status transitions (internal/tickets.MarkPaid) are idempotent.
func (c *Client) Verify(ctx context.Context, reference string) (*VerifyResult, error) {
	var out apiEnvelope[VerifyResult]
	path := "/transaction/verify/" + url.PathEscape(reference)
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return &out.Data, nil
}

// VerifyWebhookSignature reports whether signatureHex (the value of the
// X-Paystack-Signature header) is the correct HMAC-SHA512 of body, hex
// encoded, using this client's secret key. body must be the exact raw
// request bytes -- Paystack signs over what it actually sent, not a
// re-marshaled version of it, so this must be called before any
// json.Unmarshal of the request body.
func (c *Client) VerifyWebhookSignature(body []byte, signatureHex string) bool {
	if signatureHex == "" {
		return false
	}
	mac := hmac.New(sha512.New, []byte(c.SecretKey))
	mac.Write(body)
	expected := hex.EncodeToString(mac.Sum(nil))
	return subtle.ConstantTimeCompare([]byte(expected), []byte(signatureHex)) == 1
}

// WebhookEvent is the subset of a Paystack webhook payload this app reads.
type WebhookEvent struct {
	Event string `json:"event"`
	Data  struct {
		Reference string `json:"reference"`
		Status    string `json:"status"`
		Amount    int64  `json:"amount"`
	} `json:"data"`
}

type apiEnvelope[T any] struct {
	Status  bool   `json:"status"`
	Message string `json:"message"`
	Data    T      `json:"data"`
}

// ErrAPI wraps a non-2xx or status:false Paystack response with its
// message, so callers and logs get something actionable instead of a bare
// "unexpected status code".
type ErrAPI struct {
	StatusCode int
	Message    string
}

func (e *ErrAPI) Error() string {
	return fmt.Sprintf("paystack: %s (http %d)", e.Message, e.StatusCode)
}

func (c *Client) do(ctx context.Context, method, path string, body []byte, out any) error {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL()+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.SecretKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}

	var envelope struct {
		Status  bool   `json:"status"`
		Message string `json:"message"`
	}
	_ = json.Unmarshal(respBody, &envelope)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 || !envelope.Status {
		msg := envelope.Message
		if msg == "" {
			msg = "unexpected response from Paystack"
		}
		return &ErrAPI{StatusCode: resp.StatusCode, Message: msg}
	}

	if out != nil {
		if err := json.Unmarshal(respBody, out); err != nil {
			return fmt.Errorf("paystack: decode response: %w", err)
		}
	}
	return nil
}

func (c *Client) baseURL() string {
	if c.BaseURL != "" {
		return c.BaseURL
	}
	return defaultBaseURL
}

func (c *Client) httpClient() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	return http.DefaultClient
}
