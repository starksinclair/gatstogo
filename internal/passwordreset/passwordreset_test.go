package passwordreset

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

// newTestRedis mirrors internal/shifts/pinlimiter_test.go's own
// newTestLimiter -- an in-memory, real-Redis-protocol-compatible server
// for the duration of one test.
func newTestRedis(t *testing.T) *redis.Client {
	t.Helper()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func TestIssueThenConsumeReturnsTheSameUser(t *testing.T) {
	ctx := context.Background()
	rdb := newTestRedis(t)
	userID := uuid.New()

	token, err := IssueToken(ctx, rdb, userID)
	if err != nil {
		t.Fatalf("IssueToken: %v", err)
	}
	if token == "" {
		t.Fatal("expected a non-empty token")
	}

	got, err := Consume(ctx, rdb, token)
	if err != nil {
		t.Fatalf("Consume: %v", err)
	}
	if got != userID {
		t.Errorf("Consume returned %s, want %s", got, userID)
	}
}

func TestConsumeIsSingleUse(t *testing.T) {
	ctx := context.Background()
	rdb := newTestRedis(t)
	userID := uuid.New()

	token, err := IssueToken(ctx, rdb, userID)
	if err != nil {
		t.Fatalf("IssueToken: %v", err)
	}
	if _, err := Consume(ctx, rdb, token); err != nil {
		t.Fatalf("first Consume: %v", err)
	}

	if _, err := Consume(ctx, rdb, token); err != ErrInvalidOrExpired {
		t.Errorf("second Consume: expected ErrInvalidOrExpired (already consumed), got %v", err)
	}
}

func TestConsumeRejectsUnknownToken(t *testing.T) {
	ctx := context.Background()
	rdb := newTestRedis(t)

	if _, err := Consume(ctx, rdb, "not-a-real-token"); err != ErrInvalidOrExpired {
		t.Errorf("expected ErrInvalidOrExpired, got %v", err)
	}
}

func TestPeekDoesNotConsume(t *testing.T) {
	ctx := context.Background()
	rdb := newTestRedis(t)
	userID := uuid.New()

	token, err := IssueToken(ctx, rdb, userID)
	if err != nil {
		t.Fatalf("IssueToken: %v", err)
	}

	// Peek twice -- neither call should invalidate the token, unlike
	// Consume (see TestConsumeIsSingleUse above).
	for i := 0; i < 2; i++ {
		got, err := Peek(ctx, rdb, token)
		if err != nil {
			t.Fatalf("Peek #%d: %v", i, err)
		}
		if got != userID {
			t.Errorf("Peek #%d returned %s, want %s", i, got, userID)
		}
	}

	// A real Consume afterward should still succeed -- proof Peek never
	// touched the underlying key.
	if _, err := Consume(ctx, rdb, token); err != nil {
		t.Errorf("Consume after Peek: expected success, got %v", err)
	}
}

func TestPeekRejectsUnknownToken(t *testing.T) {
	ctx := context.Background()
	rdb := newTestRedis(t)

	if _, err := Peek(ctx, rdb, "not-a-real-token"); err != ErrInvalidOrExpired {
		t.Errorf("expected ErrInvalidOrExpired, got %v", err)
	}
}

func TestTokenExpires(t *testing.T) {
	ctx := context.Background()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	userID := uuid.New()
	token, err := IssueToken(ctx, rdb, userID)
	if err != nil {
		t.Fatalf("IssueToken: %v", err)
	}

	mr.FastForward(TTL + time.Minute)

	if _, err := Consume(ctx, rdb, token); err != ErrInvalidOrExpired {
		t.Errorf("expected ErrInvalidOrExpired for an expired token, got %v", err)
	}
}

func TestIssueTokenProducesDistinctTokens(t *testing.T) {
	ctx := context.Background()
	rdb := newTestRedis(t)
	userID := uuid.New()

	tokenA, err := IssueToken(ctx, rdb, userID)
	if err != nil {
		t.Fatalf("IssueToken (a): %v", err)
	}
	tokenB, err := IssueToken(ctx, rdb, userID)
	if err != nil {
		t.Fatalf("IssueToken (b): %v", err)
	}
	if tokenA == tokenB {
		t.Error("expected two separate IssueToken calls to produce distinct tokens")
	}
	// Both remain independently valid -- issuing a second reset link
	// doesn't invalidate an already-issued one still inside its TTL.
	if _, err := Consume(ctx, rdb, tokenA); err != nil {
		t.Errorf("Consume(tokenA): %v", err)
	}
	if _, err := Consume(ctx, rdb, tokenB); err != nil {
		t.Errorf("Consume(tokenB): %v", err)
	}
}
