package receipts

import (
	"context"
	"errors"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

func newTestStore(t *testing.T) *CodeStore {
	t.Helper()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	return NewCodeStore(client)
}

func TestRequestAndVerifyCode(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	plantID := uuid.New()
	phone := "08030000000"

	code, err := store.RequestCode(ctx, plantID, phone)
	if err != nil {
		t.Fatalf("RequestCode: %v", err)
	}
	if len(code) != 6 {
		t.Fatalf("expected a 6-digit code, got %q", code)
	}

	ok, err := store.VerifyCode(ctx, plantID, phone, code)
	if err != nil {
		t.Fatalf("VerifyCode: %v", err)
	}
	if !ok {
		t.Error("expected the correct code to verify")
	}
}

func TestVerifyCodeIsOneTimeUse(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	plantID := uuid.New()
	phone := "08030000000"

	code, err := store.RequestCode(ctx, plantID, phone)
	if err != nil {
		t.Fatalf("RequestCode: %v", err)
	}

	ok, err := store.VerifyCode(ctx, plantID, phone, code)
	if err != nil || !ok {
		t.Fatalf("first verify: ok=%v err=%v, want ok=true err=nil", ok, err)
	}

	ok, err = store.VerifyCode(ctx, plantID, phone, code)
	if err != nil {
		t.Fatalf("second verify: %v", err)
	}
	if ok {
		t.Error("expected the same code to fail on a second attempt (consumed after first use)")
	}
}

func TestVerifyCodeWrongCodeNotConsumed(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	plantID := uuid.New()
	phone := "08030000000"

	code, err := store.RequestCode(ctx, plantID, phone)
	if err != nil {
		t.Fatalf("RequestCode: %v", err)
	}

	if ok, err := store.VerifyCode(ctx, plantID, phone, "000000"); err != nil || ok {
		t.Fatalf("expected a wrong code to fail, got ok=%v err=%v", ok, err)
	}
	// A mistyped attempt shouldn't burn the real code -- the customer
	// should still be able to try again with the correct one.
	ok, err := store.VerifyCode(ctx, plantID, phone, code)
	if err != nil || !ok {
		t.Fatalf("expected the real code to still work after a wrong attempt, got ok=%v err=%v", ok, err)
	}
}

func TestVerifyCodeIsolatedPerPhoneAndPlant(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	plantA, plantB := uuid.New(), uuid.New()
	phone := "08030000000"

	code, err := store.RequestCode(ctx, plantA, phone)
	if err != nil {
		t.Fatalf("RequestCode: %v", err)
	}
	if ok, err := store.VerifyCode(ctx, plantB, phone, code); err != nil || ok {
		t.Fatalf("expected a code issued for plantA to not verify against plantB, got ok=%v err=%v", ok, err)
	}
	if ok, err := store.VerifyCode(ctx, plantA, "0809999999", code); err != nil || ok {
		t.Fatalf("expected a code issued for one phone to not verify for another, got ok=%v err=%v", ok, err)
	}
}

func TestVerifyCodeEmptySubmissionFails(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	plantID := uuid.New()
	if ok, err := store.VerifyCode(ctx, plantID, "08030000000", ""); err != nil || ok {
		t.Fatalf("expected an empty submitted code to fail, got ok=%v err=%v", ok, err)
	}
}

func TestVerifyCodeUnknownPhoneFails(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	if ok, err := store.VerifyCode(ctx, uuid.New(), "08030000000", "123456"); err != nil || ok {
		t.Fatalf("expected no requested code to fail verification, got ok=%v err=%v", ok, err)
	}
}

func TestVerifyCodeLocksOutAfterTooManyFailures(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	plantID := uuid.New()
	phone := "08030000000"

	code, err := store.RequestCode(ctx, plantID, phone)
	if err != nil {
		t.Fatalf("RequestCode: %v", err)
	}

	for i := 0; i < MaxVerifyAttempts; i++ {
		ok, err := store.VerifyCode(ctx, plantID, phone, "wrong-guess")
		if err != nil {
			t.Fatalf("attempt %d: unexpected error %v", i, err)
		}
		if ok {
			t.Fatalf("attempt %d: a wrong code should never verify", i)
		}
	}

	// Locked out now -- even the correct code must be refused.
	ok, err := store.VerifyCode(ctx, plantID, phone, code)
	if !errors.Is(err, ErrLocked) {
		t.Fatalf("expected ErrLocked after %d failed attempts, got ok=%v err=%v", MaxVerifyAttempts, ok, err)
	}
	if ok {
		t.Error("a locked-out phone must not verify, even with the right code")
	}
}

func TestVerifyCodeFailuresDoNotLockOutOtherPhones(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	plantID := uuid.New()

	for i := 0; i < MaxVerifyAttempts; i++ {
		if _, err := store.VerifyCode(ctx, plantID, "08030000000", "wrong-guess"); err != nil {
			t.Fatalf("attempt %d: unexpected error %v", i, err)
		}
	}

	code, err := store.RequestCode(ctx, plantID, "08099999999")
	if err != nil {
		t.Fatalf("RequestCode for a different phone: %v", err)
	}
	ok, err := store.VerifyCode(ctx, plantID, "08099999999", code)
	if err != nil || !ok {
		t.Fatalf("a different phone's own code should still verify, got ok=%v err=%v", ok, err)
	}
}

func TestRequestCodeCooldownBlocksImmediateResend(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	plantID := uuid.New()
	phone := "08030000000"

	if _, err := store.RequestCode(ctx, plantID, phone); err != nil {
		t.Fatalf("first RequestCode: %v", err)
	}
	if _, err := store.RequestCode(ctx, plantID, phone); !errors.Is(err, ErrCooldown) {
		t.Fatalf("expected ErrCooldown on an immediate resend, got %v", err)
	}
}
