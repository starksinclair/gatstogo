package sms

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const defaultTermiiBaseURL = "https://api.ng.termii.com"

// TermiiSender sends SMS messages through Termii's single-message API.
type TermiiSender struct {
	APIKey     string
	SenderID   string
	Channel    string
	BaseURL    string
	HTTPClient *http.Client
}

func NewTermiiSender(apiKey, senderID, channel, baseURL string) *TermiiSender {
	if strings.TrimSpace(channel) == "" {
		channel = "generic"
	}
	if strings.TrimSpace(baseURL) == "" {
		baseURL = defaultTermiiBaseURL
	}
	return &TermiiSender{
		APIKey:     strings.TrimSpace(apiKey),
		SenderID:   strings.TrimSpace(senderID),
		Channel:    strings.TrimSpace(channel),
		BaseURL:    strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		HTTPClient: &http.Client{Timeout: 15 * time.Second},
	}
}

// ErrAPI wraps a non-2xx Termii response.
type ErrAPI struct {
	StatusCode int
	Message    string
}

func (e *ErrAPI) Error() string {
	return fmt.Sprintf("termii: %s (http %d)", e.Message, e.StatusCode)
}

func (s *TermiiSender) Send(ctx context.Context, to, message string) error {
	return s.SendFrom(ctx, s.SenderID, to, message)
}

func (s *TermiiSender) SendFrom(ctx context.Context, senderID, to, message string) error {
	senderID = strings.TrimSpace(senderID)
	if senderID == "" {
		senderID = s.SenderID
	}
	payload, err := json.Marshal(map[string]any{
		"api_key": s.APIKey,
		"to":      termiiPhone(to),
		"from":    senderID,
		"sms":     message,
		"type":    "plain",
		"channel": s.channel(),
	})
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.baseURL()+"/api/sms/send", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.httpClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := termiiErrorMessage(respBody)
		if msg == "" {
			msg = "unexpected response from Termii"
		}
		return &ErrAPI{StatusCode: resp.StatusCode, Message: msg}
	}
	return nil
}

func (s *TermiiSender) baseURL() string {
	if s.BaseURL != "" {
		return strings.TrimRight(s.BaseURL, "/")
	}
	return defaultTermiiBaseURL
}

func (s *TermiiSender) channel() string {
	if strings.TrimSpace(s.Channel) != "" {
		return strings.TrimSpace(s.Channel)
	}
	return "generic"
}

func (s *TermiiSender) httpClient() *http.Client {
	if s.HTTPClient != nil {
		return s.HTTPClient
	}
	return http.DefaultClient
}

func termiiErrorMessage(body []byte) string {
	var envelope struct {
		Message string `json:"message"`
		Error   string `json:"error"`
	}
	_ = json.Unmarshal(body, &envelope)
	if envelope.Message != "" {
		return envelope.Message
	}
	return envelope.Error
}

func termiiPhone(phone string) string {
	digits := make([]byte, 0, len(phone))
	for _, r := range phone {
		if r >= '0' && r <= '9' {
			digits = append(digits, byte(r))
		}
	}
	switch {
	case len(digits) == 13 && string(digits[:3]) == "234":
		return string(digits)
	case len(digits) == 11 && digits[0] == '0':
		return "234" + string(digits[1:])
	case len(digits) == 10:
		return "234" + string(digits)
	default:
		return string(digits)
	}
}
