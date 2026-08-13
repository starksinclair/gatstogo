package middleware

import (
	"net/http"
	"time"

	"gatstogo/internal/csrf"
	"gatstogo/internal/session"
)

// PublicCSRFCookie carries the double-submit token for forms with no
// session yet (customer buy-gas, receipt lookup).
const PublicCSRFCookie = "gtg_csrf"

const publicCSRFCookieTTL = 24 * time.Hour

// EnsurePublicCSRFCookie returns the current PublicCSRFCookie value,
// minting and setting a new one if the request doesn't already carry one.
// Call this from a GET handler before rendering a public form, so the page
// can embed the same value as a hidden field; the browser will send the
// cookie back with the form's POST.
//
// Not HttpOnly: the page's own rendered <input type="hidden"> needs the
// value at render time (server-side, so this isn't actually a JS read of
// the cookie for that case), but a later fetch()-based caller (the staff
// terminal frontend, M9b) does need to read it via document.cookie to send
// as a header, which HttpOnly would block.
func EnsurePublicCSRFCookie(w http.ResponseWriter, r *http.Request) string {
	if c, err := r.Cookie(PublicCSRFCookie); err == nil && c.Value != "" {
		return c.Value
	}
	token, err := csrf.NewPublicToken()
	if err != nil {
		// Fail closed: an empty embedded token will never match anything
		// VerifyPublic checks it against, so the next POST is rejected
		// rather than silently left unprotected.
		return ""
	}
	http.SetCookie(w, &http.Cookie{
		Name:     PublicCSRFCookie,
		Value:    token,
		Path:     "/",
		HttpOnly: false,
		Secure:   session.SecureCookies(),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(publicCSRFCookieTTL.Seconds()),
	})
	return token
}

// CSRF rejects any mutating request (POST/PUT/PATCH/DELETE) whose CSRF
// token is missing or wrong. GET/HEAD/OPTIONS always pass through
// unconditionally -- CSRF only matters once a request changes something.
//
// Which check applies depends on where in the middleware chain this is
// mounted relative to RequireOwnerSession/RequireAdminSession:
//   - mounted *after* one of those (so GetActor(ctx) is populated): the
//     submitted token must equal csrf.Token(actor.Token) -- the
//     authenticated, session-derived check.
//   - mounted with no session middleware before it (login POSTs, and the
//     public ticket/receipt POSTs landing in M8/M12): the submitted token
//     must equal the PublicCSRFCookie value -- the double-submit check.
//
// Deliberately never mounted on POST /webhooks/paystack (M8) -- that's a
// server-to-server call with its own HMAC-SHA512 signature scheme and no
// cookies to double-submit against.
func CSRF(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			next.ServeHTTP(w, r)
			return
		}

		submitted := r.Header.Get("X-CSRF-Token")
		if submitted == "" {
			submitted = r.FormValue("csrf_token")
		}

		if actor := GetActor(r.Context()); actor != nil {
			if !csrf.Verify(actor.Token, submitted) {
				http.Error(w, "Invalid or missing CSRF token", http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
			return
		}

		cookie, err := r.Cookie(PublicCSRFCookie)
		if err != nil || !csrf.VerifyPublic(cookie.Value, submitted) {
			http.Error(w, "Invalid or missing CSRF token", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}
