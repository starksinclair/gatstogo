package email

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestResendSenderSendsExpectedRequest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/emails" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Errorf("unexpected method: %s", r.Method)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer re_test_key" {
			t.Errorf("unexpected Authorization header: %s", got)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		if body["from"] != "GatsToGo <owner@gatstogo.ng>" {
			t.Errorf("unexpected from: %v", body["from"])
		}
		to, ok := body["to"].([]any)
		if !ok || len(to) != 1 || to[0] != "ada@sunrisegas.ng" {
			t.Errorf("unexpected to: %v", body["to"])
		}
		if body["subject"] != "Reset your password" {
			t.Errorf("unexpected subject: %v", body["subject"])
		}
		if body["text"] != "Reset link: https://sunrise.gatstogo.ng/owner/reset-password?token=abc123" {
			t.Errorf("unexpected text: %v", body["text"])
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "re_abc123"})
	}))
	defer srv.Close()

	sender := NewResendSender("re_test_key", "GatsToGo <owner@gatstogo.ng>")
	sender.BaseURL = srv.URL

	err := sender.Send(context.Background(), "ada@sunrisegas.ng", "Reset your password", "Reset link: https://sunrise.gatstogo.ng/owner/reset-password?token=abc123")
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
}

func TestResendSenderUsesSandboxFromWhenUnset(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		if body["from"] != resendSandboxFrom {
			t.Errorf("expected the sandbox From address when none was configured, got %v", body["from"])
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "re_abc123"})
	}))
	defer srv.Close()

	sender := NewResendSender("re_test_key", "") // no From configured
	sender.BaseURL = srv.URL

	if err := sender.Send(context.Background(), "ada@sunrisegas.ng", "s", "b"); err != nil {
		t.Fatalf("Send: %v", err)
	}
}

func TestResendSenderAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]any{"message": "Invalid API key"})
	}))
	defer srv.Close()

	sender := NewResendSender("bad_key", "GatsToGo <owner@gatstogo.ng>")
	sender.BaseURL = srv.URL

	err := sender.Send(context.Background(), "ada@sunrisegas.ng", "s", "b")
	if err == nil {
		t.Fatal("expected an error for a 401 response")
	}
	var apiErr *ErrAPI
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected *ErrAPI, got %T: %v", err, err)
	}
	if apiErr.Message != "Invalid API key" {
		t.Errorf("unexpected message: %s", apiErr.Message)
	}
	if apiErr.StatusCode != http.StatusUnauthorized {
		t.Errorf("unexpected status code: %d", apiErr.StatusCode)
	}
}

func TestLoggingSenderNeverErrors(t *testing.T) {
	sender := NewLoggingSender()
	if err := sender.Send(context.Background(), "ada@sunrisegas.ng", "s", "b"); err != nil {
		t.Errorf("LoggingSender.Send: expected nil error, got %v", err)
	}
}
