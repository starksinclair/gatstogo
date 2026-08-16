// Package passwordreset issues and consumes single-use, time-limited
// tokens for the owner "forgot password" flow (cmd/server/auth.go's
// ownerForgotPassword*/ownerResetPassword* handlers). Redis-backed, the
// same "ephemeral, security-sensitive state with a TTL" pattern
// internal/shifts.PINLimiter already established for a different purpose
// -- a new package rather than folding into that one since this tracks a
// fundamentally different thing (a one-time credential, not a failure
// counter) with its own key prefix and lifecycle.
package passwordreset

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

// TTL is how long an issued reset link stays valid -- long enough that a
// real owner checking email on their own schedule isn't rushed, short
// enough that a stale, unused link stops being a standing risk.
const TTL = 1 * time.Hour

const keyPrefix = "password_reset:"

// ErrInvalidOrExpired is returned by Peek/Consume for a token that either
// never existed, was already consumed, or has expired -- deliberately one
// error for all three cases, not three, since a caller-facing message
// ("this link is invalid or expired") is correct and identical either
// way, and distinguishing them would only help an attacker enumerate
// which case applies.
var ErrInvalidOrExpired = errors.New("passwordreset: token is invalid or expired")

// IssueToken generates a new random token for userID and stores it,
// valid for TTL. The raw token (never the userID) is what goes in the
// emailed reset link -- Redis is the only place the token-to-user mapping
// exists.
func IssueToken(ctx context.Context, rdb *redis.Client, userID uuid.UUID) (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	token := hex.EncodeToString(raw)

	if err := rdb.Set(ctx, keyPrefix+token, userID.String(), TTL).Err(); err != nil {
		return "", err
	}
	return token, nil
}

// Peek reports whether token is currently valid, without consuming it --
// used by the GET /owner/reset-password page to show "this link is
// invalid or expired" immediately, before the visitor fills out a form
// for nothing. Deliberately non-destructive: email clients and security
// scanners routinely pre-fetch links in an email's body, and a naive
// "look up and delete" here would let that pre-fetch silently burn the
// real owner's single-use link before they ever click it themselves.
func Peek(ctx context.Context, rdb *redis.Client, token string) (uuid.UUID, error) {
	raw, err := rdb.Get(ctx, keyPrefix+token).Result()
	if errors.Is(err, redis.Nil) {
		return uuid.UUID{}, ErrInvalidOrExpired
	}
	if err != nil {
		return uuid.UUID{}, err
	}
	userID, err := uuid.Parse(raw)
	if err != nil {
		return uuid.UUID{}, ErrInvalidOrExpired
	}
	return userID, nil
}

// Consume validates and irreversibly invalidates token in one atomic
// step (Redis GETDEL), so it can never be replayed -- the only place in
// this package that actually consumes a token, called from
// POST /owner/reset-password once the visitor has actually submitted a
// new password, never from the GET that renders the form (see Peek).
func Consume(ctx context.Context, rdb *redis.Client, token string) (uuid.UUID, error) {
	raw, err := rdb.GetDel(ctx, keyPrefix+token).Result()
	if errors.Is(err, redis.Nil) {
		return uuid.UUID{}, ErrInvalidOrExpired
	}
	if err != nil {
		return uuid.UUID{}, err
	}
	userID, err := uuid.Parse(raw)
	if err != nil {
		return uuid.UUID{}, ErrInvalidOrExpired
	}
	return userID, nil
}
