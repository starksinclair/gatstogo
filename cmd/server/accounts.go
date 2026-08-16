package main

import (
	"context"
	"encoding/json"
	"log"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// This file backs GET /accounts/resolve -- a thin, public wrapper around
// s.Paystack.ResolveAccount used by both onboarding forms' JS (signup.templ,
// owner_admin.templ's "Create plant" panel; see accountResolveScript in
// web/templates/pages/account_resolve.templ) to show the account holder's
// real name before submission.
//
// This is a UX convenience, not the real enforcement point: the actual
// guarantee that a submitted bank account is real is the server-side
// ResolveAccount + CreateSubaccount call cmd/server/admin_write.go's
// provisionPlant makes at plant-creation time -- if someone bypassed this
// endpoint's JS entirely, that call fails outright for a bad account/bank
// combination, and plants.Create is never reached.

// resolveRateLimitMax/-Window cap how often a single IP can probe this
// endpoint -- repeatedly guessing account numbers to discover names is a
// known abuse pattern for exactly this kind of endpoint. Reuses the same
// Redis INCR+EXPIRE pattern internal/shifts.PINLimiter already established
// (internal/shifts/shifts.go's RecordFailure), just keyed by IP instead of
// a user id and with its own prefix/window/limit -- this isn't a login
// lockout, so it doesn't belong in that package.
const (
	resolveRateLimitMax    = 20
	resolveRateLimitWindow = 5 * time.Minute
)

func accountsResolveHandler(s *Server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		allowed, err := checkIPRateLimit(r.Context(), s.Redis, "accounts_resolve_rl:", clientIP(r), resolveRateLimitMax, resolveRateLimitWindow)
		if err != nil {
			// Fail open: a Redis hiccup shouldn't block real bank-account
			// verification for every visitor -- the actual enforcement
			// point (provisionPlant's own server-side ResolveAccount call)
			// is unaffected either way.
			log.Println("accounts resolve: rate limit check failed:", err)
		} else if !allowed {
			w.WriteHeader(http.StatusTooManyRequests)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "Too many requests. Please wait a moment and try again."})
			return
		}

		accountNumber := strings.TrimSpace(r.URL.Query().Get("account_number"))
		bankCode := strings.TrimSpace(r.URL.Query().Get("bank_code"))
		if accountNumber == "" || bankCode == "" {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "Enter an account number and choose a bank."})
			return
		}

		result, err := s.Paystack.ResolveAccount(r.Context(), accountNumber, bankCode)
		if err != nil {
			w.WriteHeader(http.StatusUnprocessableEntity)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "We couldn't verify that account number. Double-check it and try again."})
			return
		}

		_ = json.NewEncoder(w).Encode(map[string]string{"account_name": result.AccountName})
	}
}

// checkIPRateLimit caps how many times a single IP can hit whatever
// endpoint calls this within window, keyed by keyPrefix+ip -- the same
// Redis INCR+EXPIRE pattern internal/shifts.PINLimiter established for
// login lockouts, generalized here since this endpoint and
// ownerForgotPasswordSubmitHandler (cmd/server/auth.go) both need the
// exact same shape for the exact same reason (probing/spam via a public,
// unauthenticated endpoint), just with their own prefix/limit/window. A
// Redis error fails open (returns allowed=true) -- a Redis hiccup
// shouldn't block a real visitor from using a legitimate endpoint; the
// caller logs the error so a persistent problem is still visible.
func checkIPRateLimit(ctx context.Context, rdb *redis.Client, keyPrefix, ip string, max int, window time.Duration) (bool, error) {
	key := keyPrefix + ip
	n, err := rdb.Incr(ctx, key).Result()
	if err != nil {
		return true, err
	}
	if n == 1 {
		if err := rdb.Expire(ctx, key, window).Err(); err != nil {
			return true, err
		}
	}
	return n <= int64(max), nil
}

// clientIP prefers X-Forwarded-For (set by a trusted reverse proxy in any
// real deployment) over r.RemoteAddr, the same trust assumption
// requestScheme (cmd/server/tickets.go) already makes for X-Forwarded-Proto.
func clientIP(r *http.Request) string {
	if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
		parts := strings.Split(fwd, ",")
		return strings.TrimSpace(parts[0])
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
