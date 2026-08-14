// Package receipts implements the customer receipts lookup: a phone
// number, a one-time code sent by SMS (internal/sms), and -- once
// verified -- a ticket list and receipt-confirmation flow. Before this
// build-out, /receipts was static demo content with no real lookup or
// confirmation at all.
package receipts

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"errors"
	"fmt"
	"math/big"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

// CodeTTL is how long a one-time lookup code stays valid.
const CodeTTL = 10 * time.Minute

const (
	// MaxVerifyAttempts / VerifyLockoutTTL bound how many wrong codes a
	// phone number can submit before being locked out. Without this, a
	// 6-digit code (1e6 possibilities) is comfortably brute-forceable
	// within its own CodeTTL by an unrate-limited attacker -- the same
	// concern internal/shifts.PINLimiter exists for on the terminal PIN
	// side, just keyed by (plantID, phone) here instead of a user id since
	// there's no account to attach the counter to.
	MaxVerifyAttempts = 5
	VerifyLockoutTTL  = 5 * time.Minute

	// ResendCooldown is the minimum time between two RequestCode calls for
	// the same (plantID, phone) -- a floor against a form double-submit or
	// a "didn't arrive, try again" tap loop turning into an SMS-cost (and,
	// once a real internal/sms.Sender replaces LoggingSender, actual spend)
	// bombing vector.
	ResendCooldown = 60 * time.Second
)

// ErrLocked is returned by VerifyCode once MaxVerifyAttempts wrong codes
// have been submitted for a (plantID, phone) pair -- even a correct code
// is refused until VerifyLockoutTTL passes.
var ErrLocked = errors.New("receipts: too many attempts, try again later")

// ErrCooldown is returned by RequestCode when called again for the same
// (plantID, phone) before ResendCooldown has passed since the last call.
var ErrCooldown = errors.New("receipts: a code was already sent recently, wait before requesting another")

// CodeStore issues and verifies one-time lookup codes in Redis -- the
// same kind of short-lived, high-churn, non-relational data
// internal/shifts.PINLimiter already uses Redis for, rather than the
// tickets/users tables.
type CodeStore struct {
	rdb *redis.Client
}

func NewCodeStore(rdb *redis.Client) *CodeStore {
	return &CodeStore{rdb: rdb}
}

// RequestCode generates a 6-digit one-time code for phone at plantID and
// stores it for CodeTTL. The caller is responsible for actually sending
// it (internal/sms) -- this function never returns the code to anything
// other than the caller that's about to send it; it must never be
// echoed back in an HTTP response. Returns ErrCooldown if the last call
// for this (plantID, phone) was less than ResendCooldown ago.
func (s *CodeStore) RequestCode(ctx context.Context, plantID uuid.UUID, phone string) (string, error) {
	acquired, err := s.rdb.SetNX(ctx, cooldownKey(plantID, phone), "1", ResendCooldown).Result()
	if err != nil {
		return "", err
	}
	if !acquired {
		return "", ErrCooldown
	}

	code, err := newCode()
	if err != nil {
		return "", err
	}
	if err := s.rdb.Set(ctx, codeKey(plantID, phone), code, CodeTTL).Err(); err != nil {
		return "", err
	}
	return code, nil
}

// VerifyCode reports whether submitted is the current, unexpired code for
// phone at plantID. Consumes the code on a successful match (one-time
// use -- a second attempt with the same code fails even if it hasn't
// expired yet), but deliberately does not consume it on a failed
// attempt, so a mistyped digit doesn't force requesting a whole new code.
// Returns ErrLocked (not just ok=false) once MaxVerifyAttempts wrong
// guesses have accumulated -- see the lockout constants above.
func (s *CodeStore) VerifyCode(ctx context.Context, plantID uuid.UUID, phone, submitted string) (bool, error) {
	if submitted == "" {
		return false, nil
	}

	locked, err := s.rdb.Exists(ctx, lockKey(plantID, phone)).Result()
	if err != nil {
		return false, err
	}
	if locked > 0 {
		return false, ErrLocked
	}

	key := codeKey(plantID, phone)
	stored, err := s.rdb.Get(ctx, key).Result()
	if errors.Is(err, redis.Nil) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if subtle.ConstantTimeCompare([]byte(stored), []byte(submitted)) != 1 {
		_ = s.recordFailure(ctx, plantID, phone)
		return false, nil
	}
	_ = s.rdb.Del(ctx, key)
	_ = s.rdb.Del(ctx, failKey(plantID, phone))
	return true, nil
}

// recordFailure increments the wrong-guess counter for (plantID, phone)
// and, once it reaches MaxVerifyAttempts, sets the lockout key VerifyCode
// checks above. The counter itself expires with CodeTTL -- it shouldn't
// outlive the code it's counting guesses against.
func (s *CodeStore) recordFailure(ctx context.Context, plantID uuid.UUID, phone string) error {
	key := failKey(plantID, phone)
	n, err := s.rdb.Incr(ctx, key).Result()
	if err != nil {
		return err
	}
	if n == 1 {
		if err := s.rdb.Expire(ctx, key, CodeTTL).Err(); err != nil {
			return err
		}
	}
	if n >= MaxVerifyAttempts {
		return s.rdb.Set(ctx, lockKey(plantID, phone), "1", VerifyLockoutTTL).Err()
	}
	return nil
}

func codeKey(plantID uuid.UUID, phone string) string {
	return "receipt_code:" + plantID.String() + ":" + phone
}

func failKey(plantID uuid.UUID, phone string) string {
	return "receipt_code_fail:" + plantID.String() + ":" + phone
}

func lockKey(plantID uuid.UUID, phone string) string {
	return "receipt_code_lock:" + plantID.String() + ":" + phone
}

func cooldownKey(plantID uuid.UUID, phone string) string {
	return "receipt_code_cooldown:" + plantID.String() + ":" + phone
}

func newCode() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(1000000))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%06d", n.Int64()), nil
}
