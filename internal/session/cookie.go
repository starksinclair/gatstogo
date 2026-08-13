package session

import (
	"net/http"
	"os"
	"time"
)

// CookieName is the single session cookie used for every login type (owner,
// admin, staff terminal) -- which session a token belongs to is determined
// by the Role stored against it in Redis, not by which cookie carried it.
const CookieName = "gtg_session"

// SecureCookies reports whether the Secure flag should be set on cookies.
// Defaults to true (fail safe) so a misconfigured APP_ENV never
// accidentally ships a cookie over plain HTTP in production; opts out only
// for the local dev loop (http://sunrise.localhost:8080), where there's no
// TLS to require. Exported so internal/middleware's CSRF double-submit
// cookie (which has the same requirement but isn't a login session) stays
// consistent with this instead of duplicating the check.
func SecureCookies() bool {
	return os.Getenv("APP_ENV") != "development"
}

// WriteCookie is the single choke point for setting the session cookie, so
// its flags can never drift between the owner-login, admin-login, and
// staff-PIN-login code paths.
//
// Deliberately host-only: no Domain attribute is set. A cookie scoped to a
// parent domain (e.g. Domain=.gatstogo.ng) would be sent to every tenant's
// subdomain, meaning a session minted on sunrise.gatstogo.ng could
// physically arrive at acme.gatstogo.ng. Leaving Domain unset makes that
// structurally impossible, on top of whatever server-side role/plant_id
// checks a handler does -- real defense in depth, not just a convention.
//
// SameSite=Lax rather than Strict: Strict would drop the cookie on the
// top-level cross-site redirect back from Paystack's hosted checkout to our
// own callback route (M8), which needs the customer's in-progress session
// to still be there when they land back on our domain.
func WriteCookie(w http.ResponseWriter, token string, ttl time.Duration) {
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   SecureCookies(),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(ttl.Seconds()),
	})
}

// ClearCookie expires the session cookie immediately. Flags must match
// WriteCookie's exactly, or some browsers will treat this as setting a
// second, different cookie rather than clearing the existing one.
func ClearCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   SecureCookies(),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}

// ReadCookie returns the raw session token from the request, if present.
func ReadCookie(r *http.Request) (string, bool) {
	c, err := r.Cookie(CookieName)
	if err != nil || c.Value == "" {
		return "", false
	}
	return c.Value, true
}
