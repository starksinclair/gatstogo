package middleware

import (
	"context"
	"net/http"

	"gatstogo/internal/session"

	"github.com/google/uuid"
)

// ActorContextKey reuses the same contextKey type PlantContextKey (in
// tenant.go) is defined with -- both live in this package, so there's no
// reason to introduce a second key type.
const ActorContextKey contextKey = "actor"

// Actor is the authenticated identity attached to the request context by
// RequireOwnerSession / RequireAdminSession (and, in a later milestone,
// a staff-terminal equivalent tied to an open shift).
type Actor struct {
	UserID  uuid.UUID
	Name    string
	Role    session.Role
	PlantID *uuid.UUID // nil for admin sessions (see session.Data.PlantID)
	Token   string     // the raw session token -- needed for logout
}

// GetActor returns the authenticated actor for this request, or nil if
// none of the RequireXSession middlewares ran (or none matched).
func GetActor(ctx context.Context) *Actor {
	actor, ok := ctx.Value(ActorContextKey).(*Actor)
	if !ok {
		return nil
	}
	return actor
}

// RequireOwnerSession gates a route to a logged-in owner or manager of the
// tenant this request already resolved a plant for. It must run after
// Tenant in the middleware chain -- it reads GetPlant(ctx) and 404s if
// nothing set it.
//
// On any failure (no cookie, unknown/expired session, wrong role, or a
// session whose plant_id doesn't match the plant this request resolved to)
// it redirects to the tenant's /owner/login rather than returning a bare
// 401: these are ordinary browser-navigated pages, not a JSON API. A
// plant_id mismatch specifically would mean the host-only cookie scoping in
// internal/session somehow failed -- treating it identically to "not logged
// in" (rather than, say, granting read access) is deliberate: there's no
// scenario where serving another tenant's dashboard to it is the right
// fallback.
func RequireOwnerSession(store *session.Store) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			plant := GetPlant(r.Context())
			if plant == nil {
				http.Error(w, "Plant not found", http.StatusNotFound)
				return
			}

			token, ok := session.ReadCookie(r)
			if !ok {
				http.Redirect(w, r, "/owner/login", http.StatusSeeOther)
				return
			}
			data, err := store.Load(r.Context(), token)
			if err != nil {
				http.Redirect(w, r, "/owner/login", http.StatusSeeOther)
				return
			}
			if (data.Role != session.RoleOwner && data.Role != session.RoleManager) || data.PlantID != plant.ID.String() {
				http.Redirect(w, r, "/owner/login", http.StatusSeeOther)
				return
			}
			userID, err := uuid.Parse(data.UserID)
			if err != nil {
				http.Redirect(w, r, "/owner/login", http.StatusSeeOther)
				return
			}

			// Sliding expiry: an owner actively using the dashboard should
			// never be logged out mid-session.
			_ = store.Touch(r.Context(), token, session.OwnerTTL)

			plantID := plant.ID
			actor := &Actor{UserID: userID, Name: data.Name, Role: data.Role, PlantID: &plantID, Token: token}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ActorContextKey, actor)))
		})
	}
}

// RequireAdminSession gates a route to a logged-in platform admin. Unlike
// RequireOwnerSession, this does not run inside the tenant middleware group
// -- there is no plant to check a session against, matching /admin's
// existing, deliberate placement outside that group.
func RequireAdminSession(store *session.Store) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token, ok := session.ReadCookie(r)
			if !ok {
				http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
				return
			}
			data, err := store.Load(r.Context(), token)
			if err != nil || data.Role != session.RoleAdmin {
				http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
				return
			}
			userID, err := uuid.Parse(data.UserID)
			if err != nil {
				http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
				return
			}

			_ = store.Touch(r.Context(), token, session.OwnerTTL)

			actor := &Actor{UserID: userID, Name: data.Name, Role: data.Role, Token: token}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ActorContextKey, actor)))
		})
	}
}
