package middleware

import (
	"context"
	"encoding/json"
	"net/http"

	"gatstogo/internal/session"

	"github.com/google/uuid"
)

// ActorContextKey reuses the same contextKey type PlantContextKey (in
// tenant.go) is defined with -- both live in this package, so there's no
// reason to introduce a second key type.
const ActorContextKey contextKey = "actor"

// Actor is the authenticated identity attached to the request context by
// RequireOwnerSession / RequireAdminSession / RequireStaffSession.
type Actor struct {
	UserID  uuid.UUID
	Name    string
	Role    session.Role
	PlantID *uuid.UUID // nil for admin sessions (see session.Data.PlantID)
	ShiftID *uuid.UUID // set only for staff terminal sessions (RequireStaffSession)
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

// RequireStaffSession gates a route to a logged-in cashier, operator, or
// manager with an open shift on this plant. Must run after Tenant.
//
// Unlike RequireOwnerSession/RequireAdminSession, failures respond with a
// small JSON body instead of a redirect: the staff terminal is a
// fetch()-driven single-page UI (see cmd/server/terminal.go), not a
// full-page browser navigation, so a 30x here would just hand the
// frontend's fetch() call an HTML login page it has no way to render.
func RequireStaffSession(store *session.Store) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			plant := GetPlant(r.Context())
			if plant == nil {
				http.Error(w, "Plant not found", http.StatusNotFound)
				return
			}

			token, ok := session.ReadCookie(r)
			if !ok {
				writeSessionError(w, http.StatusUnauthorized, "not signed in")
				return
			}
			data, err := store.Load(r.Context(), token)
			if err != nil {
				writeSessionError(w, http.StatusUnauthorized, "session expired")
				return
			}
			staffRole := data.Role == session.RoleManager || data.Role == session.RoleCashier || data.Role == session.RoleOperator
			if !staffRole || data.PlantID != plant.ID.String() || data.ShiftID == "" {
				writeSessionError(w, http.StatusUnauthorized, "not signed in")
				return
			}
			userID, err := uuid.Parse(data.UserID)
			if err != nil {
				writeSessionError(w, http.StatusUnauthorized, "session invalid")
				return
			}
			shiftID, err := uuid.Parse(data.ShiftID)
			if err != nil {
				writeSessionError(w, http.StatusUnauthorized, "session invalid")
				return
			}

			// Renews the leak-prevention backstop, not a primary expiry --
			// see session.StaffBackstopTTL's doc comment. The session is
			// deleted outright the moment shift-close succeeds
			// (cmd/server/terminal.go), which is the real end-of-life path.
			_ = store.Touch(r.Context(), token, session.StaffBackstopTTL)

			plantID := plant.ID
			actor := &Actor{UserID: userID, Name: data.Name, Role: data.Role, PlantID: &plantID, ShiftID: &shiftID, Token: token}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ActorContextKey, actor)))
		})
	}
}

func writeSessionError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}
