package attendant

import (
	"net/http"
	"strconv"

	"gatstogo/internal/config"
	"gatstogo/internal/middleware"
	"gatstogo/web/templates/pages"

	"github.com/go-chi/chi/v5"
)

func defaultTabFor(config string) string {
	if config == "cashier" {
		return "cash"
	}
	return "fill"
}

// Dashboard renders the full authenticated terminal shell.
func Dashboard(deps *config.Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		plant := middleware.GetPlant(r.Context())
		user := middleware.GetUser(r.Context())
		shift := middleware.GetShift(r.Context())

		data := buildShellData(r.Context(), deps, plant, user, shift, defaultTabFor(roleToConfig(user.Role)))
		data.Cash = buildCashPanel(r.Context(), deps, shift, "", "", "")

		if err := pages.StaffTerminalDashboard(data).Render(r.Context(), w); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	}
}

// DashboardTab renders just the dashboard shell for the requested tab,
// swapped in over #dashboard-shell.
func DashboardTab(deps *config.Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		plant := middleware.GetPlant(r.Context())
		user := middleware.GetUser(r.Context())
		shift := middleware.GetShift(r.Context())

		tab := chi.URLParam(r, "tab")
		if tab != "fill" && tab != "cash" && tab != "end" {
			tab = "fill"
		}
		if tab == "cash" && roleToConfig(user.Role) == "operator" {
			tab = "fill"
		}

		renderShell(w, r, deps, plant, user, shift, tab, nil, nil)
	}
}

// EndShift closes the current shift, records the closing cash count, and
// returns the attendant to the login screen.
func EndShift(deps *config.Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		plant := middleware.GetPlant(r.Context())
		user := middleware.GetUser(r.Context())
		shift := middleware.GetShift(r.Context())

		if err := r.ParseForm(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		closingCashKobo := int64(0)
		if n, err := strconv.Atoi(normalizeDigits(r.FormValue("closing_cash"))); err == nil {
			closingCashKobo = int64(n) * 100
		}

		tx, err := deps.AppDB.Begin(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		defer tx.Rollback(r.Context())

		if _, err := tx.Exec(r.Context(), `
			UPDATE shifts SET closed_at = now(), closing_cash_kobo = $1
			WHERE id = $2 AND plant_id = $3 AND user_id = $4 AND closed_at IS NULL
		`, closingCashKobo, shift.ID, plant.ID, user.ID); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		if _, err := tx.Exec(r.Context(), `
			INSERT INTO cash_movements (plant_id, shift_id, kind, amount_kobo, counted_kobo, recorded_by)
			VALUES ($1, $2, 'closing', 0, $3, $4)
		`, plant.ID, shift.ID, closingCashKobo, user.ID); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		if _, err := tx.Exec(r.Context(), `
			INSERT INTO activity_log (plant_id, actor_id, action, subject)
			VALUES ($1, $2, 'shift_closed', $3)
		`, plant.ID, user.ID, user.Name); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		if err := tx.Commit(r.Context()); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		deps.SessionManager.Remove(r.Context(), middleware.SessionShiftIDKey)

		w.Header().Set("HX-Redirect", "/terminal/login")
		w.WriteHeader(http.StatusOK)
	}
}
