package middleware

import (
	"context"
	"net/http"

	"gatstogo/internal/config"
	"gatstogo/internal/domain"

	"github.com/google/uuid"
)

const (
	UserContextKey  contextKey = "terminal_user"
	ShiftContextKey contextKey = "terminal_shift"

	SessionUserIDKey  = "user_id"
	SessionPlantIDKey = "plant_id"
	SessionShiftIDKey = "shift_id"
)

// Auth requires an authenticated staff-terminal session for the current
// plant. It loads the signed-in user and, if one is open, their current
// shift into the request context. It must run after middleware.Tenant.
func Auth(deps *config.Dependencies) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			plant := GetPlant(r.Context())
			if plant == nil {
				http.Error(w, "Plant not found", http.StatusNotFound)
				return
			}

			sm := deps.SessionManager
			userIDStr := sm.GetString(r.Context(), SessionUserIDKey)
			plantIDStr := sm.GetString(r.Context(), SessionPlantIDKey)
			if userIDStr == "" || plantIDStr != plant.ID.String() {
				redirectToLogin(w, r)
				return
			}

			userID, err := uuid.Parse(userIDStr)
			if err != nil {
				redirectToLogin(w, r)
				return
			}

			var user domain.User
			err = deps.AppDB.QueryRow(r.Context(), `
				SELECT id, plant_id, name, phone, role, pin_hash, password_hash, active, created_at, updated_at
				FROM users
				WHERE id = $1 AND plant_id = $2 AND active
			`, userID, plant.ID).Scan(
				&user.ID, &user.PlantID, &user.Name, &user.Phone, &user.Role,
				&user.PinHash, &user.PasswordHash, &user.Active, &user.CreatedAt, &user.UpdatedAt,
			)
			if err != nil {
				sm.Destroy(r.Context())
				redirectToLogin(w, r)
				return
			}

			ctx := context.WithValue(r.Context(), UserContextKey, &user)

			if shiftIDStr := sm.GetString(r.Context(), SessionShiftIDKey); shiftIDStr != "" {
				if shiftID, err := uuid.Parse(shiftIDStr); err == nil {
					var shift domain.Shift
					err = deps.AppDB.QueryRow(r.Context(), `
						SELECT id, plant_id, user_id, device_id, opened_at, closed_at,
						       opening_cash_kobo, closing_cash_kobo, tokens_issued, tokens_returned,
						       notes, created_at, updated_at
						FROM shifts
						WHERE id = $1 AND plant_id = $2 AND user_id = $3 AND closed_at IS NULL
					`, shiftID, plant.ID, user.ID).Scan(
						&shift.ID, &shift.PlantID, &shift.UserID, &shift.DeviceID, &shift.OpenedAt, &shift.ClosedAt,
						&shift.OpeningCashKobo, &shift.ClosingCashKobo, &shift.TokensIssued, &shift.TokensReturned,
						&shift.Notes, &shift.CreatedAt, &shift.UpdatedAt,
					)
					if err == nil {
						ctx = context.WithValue(ctx, ShiftContextKey, &shift)
					}
				}
			}

			// No open shift on record (e.g. it was ended from another
			// device) - back to the login/start-shift screen.
			if ctx.Value(ShiftContextKey) == nil {
				redirectToLogin(w, r)
				return
			}

			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// GetUser returns the authenticated staff-terminal user from context, if any.
func GetUser(ctx context.Context) *domain.User {
	user, ok := ctx.Value(UserContextKey).(*domain.User)
	if !ok {
		return nil
	}
	return user
}

// GetShift returns the authenticated user's currently open shift, if any.
func GetShift(ctx context.Context) *domain.Shift {
	shift, ok := ctx.Value(ShiftContextKey).(*domain.Shift)
	if !ok {
		return nil
	}
	return shift
}

func redirectToLogin(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Redirect", "/terminal/login")
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, "/terminal/login", http.StatusSeeOther)
}
