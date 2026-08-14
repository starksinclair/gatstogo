package shifts

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

// newTestLimiter spins up an in-memory, real-Redis-protocol-compatible
// server (miniredis) for the duration of one test -- this exercises the
// actual INCR/EXPIRE/SET/TTL/DEL calls PINLimiter makes against a real
// Redis client, just without needing a Redis server actually running in
// this environment.
func newTestLimiter(t *testing.T) *PINLimiter {
	t.Helper()
	mr := miniredis.RunT(t) // t.Cleanup-registered, torn down automatically
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	return NewPINLimiter(client)
}

func TestPINLimiterLocksOutAfterMaxAttempts(t *testing.T) {
	ctx := context.Background()
	limiter := newTestLimiter(t)
	userID := uuid.New()

	locked, _, err := limiter.Locked(ctx, userID)
	if err != nil {
		t.Fatalf("Locked: %v", err)
	}
	if locked {
		t.Fatal("expected not locked before any failures")
	}

	for i := 0; i < MaxPINAttempts-1; i++ {
		if err := limiter.RecordFailure(ctx, userID); err != nil {
			t.Fatalf("RecordFailure %d: %v", i, err)
		}
		locked, _, err := limiter.Locked(ctx, userID)
		if err != nil {
			t.Fatalf("Locked: %v", err)
		}
		if locked {
			t.Fatalf("expected not locked after only %d failures (max is %d)", i+1, MaxPINAttempts)
		}
	}

	// The MaxPINAttempts-th failure should trip the lock.
	if err := limiter.RecordFailure(ctx, userID); err != nil {
		t.Fatalf("RecordFailure (final): %v", err)
	}
	locked, retryAfter, err := limiter.Locked(ctx, userID)
	if err != nil {
		t.Fatalf("Locked: %v", err)
	}
	if !locked {
		t.Fatalf("expected locked out after %d failures", MaxPINAttempts)
	}
	if retryAfter <= 0 || retryAfter > LockoutTTL {
		t.Errorf("expected retryAfter in (0, %v], got %v", LockoutTTL, retryAfter)
	}
}

func TestPINLimiterResetClearsFailures(t *testing.T) {
	ctx := context.Background()
	limiter := newTestLimiter(t)
	userID := uuid.New()

	for i := 0; i < MaxPINAttempts-1; i++ {
		if err := limiter.RecordFailure(ctx, userID); err != nil {
			t.Fatalf("RecordFailure: %v", err)
		}
	}

	// A correct PIN entry resets the counter -- one more failure right
	// after should not trip the lock, since the count restarted from zero.
	if err := limiter.Reset(ctx, userID); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	if err := limiter.RecordFailure(ctx, userID); err != nil {
		t.Fatalf("RecordFailure after reset: %v", err)
	}
	locked, _, err := limiter.Locked(ctx, userID)
	if err != nil {
		t.Fatalf("Locked: %v", err)
	}
	if locked {
		t.Error("expected not locked -- Reset should have cleared the earlier failures")
	}
}

func TestPINLimiterIsolatedPerUser(t *testing.T) {
	ctx := context.Background()
	limiter := newTestLimiter(t)
	userA, userB := uuid.New(), uuid.New()

	for i := 0; i < MaxPINAttempts; i++ {
		if err := limiter.RecordFailure(ctx, userA); err != nil {
			t.Fatalf("RecordFailure: %v", err)
		}
	}

	lockedA, _, err := limiter.Locked(ctx, userA)
	if err != nil {
		t.Fatalf("Locked userA: %v", err)
	}
	if !lockedA {
		t.Fatal("expected userA locked out")
	}

	lockedB, _, err := limiter.Locked(ctx, userB)
	if err != nil {
		t.Fatalf("Locked userB: %v", err)
	}
	if lockedB {
		t.Error("expected userB unaffected by userA's failed attempts")
	}
}
