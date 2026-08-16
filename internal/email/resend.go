package email

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

const defaultResendBaseURL = "https://api.resend.com"

// resendSandboxFrom is Resend's own shared testing sender address --
// usable immediately with just an API key, no domain verification step
// required first. Good enough to actually deliver a password-reset email
// while getting started; verify a real sending domain with Resend later
// for better deliverability (their own dashboard walks through this) and
// point RESEND_FROM_EMAIL (.env.example) at it instead.
const resendSandboxFrom = "GatsToGo <onboarding@resend.dev>"

// ResendSender sends real email through Resend's API
// (https://resend.com/docs/api-reference/emails/send-email).
type ResendSender struct {
	APIKey     string
	From       string // defaults to resendSandboxFrom if empty
	BaseURL    string // defaults to the real Resend API; overridden in tests
	HTTPClient *http.Client
}

func NewResendSender(apiKey, from string) *ResendSender {
	return &ResendSender{
		APIKey:     apiKey,
		From:       from,
		HTTPClient: &http.Client{Timeout: 15 * time.Second},
	}
}

// ErrAPI wraps a non-2xx Resend response with its message, the same
// pattern internal/payments/paystack.go's own ErrAPI already established
// for this codebase's other external API clients.
type ErrAPI struct {
	StatusCode int
	Message    string
}

func (e *ErrAPI) Error() string {
	return fmt.Sprintf("resend: %s (http %d)", e.Message, e.StatusCode)
}

func (c *ResendSender) Send(ctx context.Context, to, subject, body string) error {
	from := c.From
	if from == "" {
		from = resendSandboxFrom
	}

	payload, err := json.Marshal(map[string]any{
		"from":    from,
		"to":      []string{to},
		"subject": subject,
		"text":    body,
	})
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL()+"/emails", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
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

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var envelope struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(respBody, &envelope)
		msg := envelope.Message
		if msg == "" {
			msg = "unexpected response from Resend"
		}
		return &ErrAPI{StatusCode: resp.StatusCode, Message: msg}
	}
	return nil
}

func (c *ResendSender) baseURL() string {
	if c.BaseURL != "" {
		return c.BaseURL
	}
	return defaultResendBaseURL
}

func (c *ResendSender) httpClient() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	return http.DefaultClient
}
