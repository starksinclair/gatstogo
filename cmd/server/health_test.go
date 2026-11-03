package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHealthHandler(t *testing.T) {
	ok := func(context.Context) error { return nil }
	fail := func(context.Context) error { return errors.New("dial tcp secret-host:5432: refused") }

	t.Run("all healthy", func(t *testing.T) {
		h := healthHandler([]healthCheck{{"database", ok}, {"redis", ok}})
		rec := httptest.NewRecorder()
		h(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		if !strings.Contains(rec.Body.String(), `"ok"`) {
			t.Errorf("body = %q, want status ok", rec.Body.String())
		}
	})

	t.Run("failing dependency is named but its error is not leaked", func(t *testing.T) {
		h := healthHandler([]healthCheck{{"database", ok}, {"redis", fail}})
		rec := httptest.NewRecorder()
		h(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want 503", rec.Code)
		}
		body := rec.Body.String()
		if !strings.Contains(body, "redis") || strings.Contains(body, "database") {
			t.Errorf("body = %q, want only redis listed as failing", body)
		}
		if strings.Contains(body, "secret-host") {
			t.Errorf("body leaks the underlying error: %q", body)
		}
	})

	t.Run("head has status but no body", func(t *testing.T) {
		h := healthHandler([]healthCheck{{"redis", fail}})
		rec := httptest.NewRecorder()
		h(rec, httptest.NewRequest(http.MethodHead, "/healthz", nil))
		if rec.Code != http.StatusServiceUnavailable || rec.Body.Len() != 0 {
			t.Errorf("HEAD = %d with %d body bytes, want 503 and none", rec.Code, rec.Body.Len())
		}
	})

	t.Run("hung dependency fails instead of hanging the probe", func(t *testing.T) {
		hang := func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }
		h := healthHandler([]healthCheck{{"database", hang}})
		rec := httptest.NewRecorder()
		start := time.Now()
		h(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
		if rec.Code != http.StatusServiceUnavailable {
			t.Errorf("status = %d, want 503", rec.Code)
		}
		if time.Since(start) > healthTimeout+time.Second {
			t.Errorf("probe took %v, want it bounded by %v", time.Since(start), healthTimeout)
		}
	})
}
