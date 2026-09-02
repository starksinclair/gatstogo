package session

import (
	"net/http"
	"os"
	"time"
)

// Cookie kinds are separate so owner, admin, staff-terminal, and receipts
// sessions on the same host don't overwrite each other. Before this split,
// a single gtg_session cookie meant opening /terminal or /receipts on
// sunrise.localhost would silently replace an owner login and look like a
// "random logout" on the next /owner refresh.
type CookieKind string

const (
	CookieOwner    CookieKind = "gtg_owner_session"
	CookieAdmin    CookieKind = "gtg_admin_session"
	CookieStaff    CookieKind = "gtg_staff_session"
	CookieReceipts CookieKind = "gtg_receipts_session"

	// legacyCookieName is the pre-split shared cookie. Cleared whenever we
	// write/clear a typed cookie so browsers don't keep sending a stale
	// token that no longer maps to the right login surface.
	legacyCookieName = "gtg_session"
)

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

// WriteCookie is the single choke point for setting a session cookie, so
// its flags can never drift between the owner-login, admin-login,
// staff-PIN-login, and receipts code paths.
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
func WriteCookie(w http.ResponseWriter, kind CookieKind, token string, ttl time.Duration) {
	setSessionCookie(w, string(kind), token, int(ttl.Seconds()))
	// Drop the old shared cookie so it can't fight the typed ones.
	setSessionCookie(w, legacyCookieName, "", -1)
}

// ClearCookie expires the given session cookie immediately. Flags must
// match WriteCookie's exactly, or some browsers will treat this as setting
// a second, different cookie rather than clearing the existing one.
func ClearCookie(w http.ResponseWriter, kind CookieKind) {
	setSessionCookie(w, string(kind), "", -1)
	setSessionCookie(w, legacyCookieName, "", -1)
}

// ReadCookie returns the raw session token for kind from the request, if
// present.
func ReadCookie(r *http.Request, kind CookieKind) (string, bool) {
	c, err := r.Cookie(string(kind))
	if err != nil || c.Value == "" {
		return "", false
	}
	return c.Value, true
}

func setSessionCookie(w http.ResponseWriter, name, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     "/",
		HttpOnly: true,
		Secure:   SecureCookies(),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   maxAge,
	})
}
