package sms

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"testing"
)

func TestTermiiSenderSendsExpectedRequest(t *testing.T) {
	sender := NewTermiiSender("termii_test_key", "GatsToGo", "", "https://termii.test")
	sender.HTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/api/sms/send" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Errorf("unexpected method: %s", r.Method)
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("unexpected Content-Type: %s", got)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		if body["api_key"] != "termii_test_key" {
			t.Errorf("unexpected api_key: %v", body["api_key"])
		}
		if body["to"] != "2348030000000" {
			t.Errorf("unexpected to: %v", body["to"])
		}
		if body["from"] != "GatsToGo" {
			t.Errorf("unexpected from: %v", body["from"])
		}
		if body["sms"] != "Approved" {
			t.Errorf("unexpected sms: %v", body["sms"])
		}
		if body["type"] != "plain" {
			t.Errorf("unexpected type: %v", body["type"])
		}
		if body["channel"] != "generic" {
			t.Errorf("unexpected channel: %v", body["channel"])
		}
		bodyBytes, _ := json.Marshal(map[string]any{"message_id": "1690896018417588518", "message": "Successfully Sent"})
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(bytes.NewReader(bodyBytes)),
		}, nil
	})}
	if err := sender.Send(context.Background(), "0803 000 0000", "Approved"); err != nil {
		t.Fatalf("Send: %v", err)
	}
}

func TestTermiiSenderAPIError(t *testing.T) {
	sender := NewTermiiSender("bad_key", "GatsToGo", "dnd", "https://termii.test")
	sender.HTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		bodyBytes, _ := json.Marshal(map[string]any{"message": "Invalid API key"})
		return &http.Response{
			StatusCode: http.StatusUnauthorized,
			Header:     make(http.Header),
			Body:       io.NopCloser(bytes.NewReader(bodyBytes)),
		}, nil
	})}
	err := sender.Send(context.Background(), "2348030000000", "Approved")
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

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func TestTermiiPhone(t *testing.T) {
	cases := map[string]string{
		"0803 000 0000":     "2348030000000",
		"8030000000":        "2348030000000",
		"2348030000000":     "2348030000000",
		"+234 803 000 0000": "2348030000000",
		"12345":             "12345",
	}
	for in, want := range cases {
		if got := termiiPhone(in); got != want {
			t.Errorf("termiiPhone(%q) = %q, want %q", in, got, want)
		}
	}
}
