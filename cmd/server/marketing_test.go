package main

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func TestAllowSignupAttempt(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	ctx := context.Background()

	for i := 1; i <= signupRateLimitMax; i++ {
		if !allowSignupAttempt(ctx, rdb, "203.0.113.7") {
			t.Fatalf("attempt %d was blocked, want it allowed (limit %d)", i, signupRateLimitMax)
		}
	}
	if allowSignupAttempt(ctx, rdb, "203.0.113.7") {
		t.Errorf("attempt %d was allowed, want it blocked", signupRateLimitMax+1)
	}
	if !allowSignupAttempt(ctx, rdb, "198.51.100.9") {
		t.Errorf("a different IP should have its own budget")
	}

	// A Redis outage must not stop real applications.
	mr.Close()
	if !allowSignupAttempt(ctx, rdb, "192.0.2.1") {
		t.Errorf("rate limiter should fail open when Redis is unreachable")
	}
}
