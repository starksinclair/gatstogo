// Package csrf implements CSRF token generation/verification for the two
// kinds of forms this app now has: authenticated ones (owner/admin/staff,
// all of which have a session from internal/session) and public ones
// (customer buy-gas, receipt lookup -- no session exists yet when they're
// submitted).
package csrf

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
)

// Secret is the server-wide HMAC key authenticated CSRF tokens are derived
// from. Must be set once at startup (main.go, from SESSION_HMAC_SECRET)
// before Token/Verify are called.
var Secret []byte

// ErrNoSecret is returned by Token if Secret hasn't been configured.
var ErrNoSecret = errors.New("csrf: Secret is not configured")

// Token derives the CSRF token for an authenticated session:
// base64url(HMAC-SHA256(Secret, sessionToken)). Deterministic and specific
// to that one session, so it needs no separate storage or expiry of its
// own -- it's valid for exactly as long as the session token it's derived
// from is (i.e. until that session is deleted or its Redis TTL lapses).
func Token(sessionToken string) (string, error) {
	if len(Secret) == 0 {
		return "", ErrNoSecret
	}
	mac := hmac.New(sha256.New, Secret)
	mac.Write([]byte(sessionToken))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

// Verify reports whether submitted is the correct CSRF token for
// sessionToken. Constant-time; folds every failure mode (no secret
// configured, empty input, mismatch) into false.
func Verify(sessionToken, submitted string) bool {
	if submitted == "" {
		return false
	}
	expected, err := Token(sessionToken)
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(expected), []byte(submitted)) == 1
}

// NewPublicToken generates a random token for the public, no-session
// double-submit-cookie flow. There's no per-session derivation possible
// here (there is no session yet); the cookie value itself *is* the secret,
// and VerifyPublic just checks the submitted value matches it.
func NewPublicToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// VerifyPublic reports whether submitted matches cookieValue, for the
// no-session double-submit-cookie flow. Constant-time.
func VerifyPublic(cookieValue, submitted string) bool {
	if cookieValue == "" || submitted == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(cookieValue), []byte(submitted)) == 1
}
