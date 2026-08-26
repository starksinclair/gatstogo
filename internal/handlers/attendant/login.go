package attendant

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"gatstogo/internal/config"
	"gatstogo/internal/middleware"
	"gatstogo/web/templates/pages"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
)

func loadEmployees(w http.ResponseWriter, r *http.Request, deps *config.Dependencies, plantID uuid.UUID) ([]pages.LoginEmployeeRow, bool) {
	rows, err := deps.AppDB.Query(r.Context(), `
		SELECT id, name, role
		FROM users
		WHERE plant_id = $1 AND active AND role IN ('owner', 'manager', 'cashier', 'operator')
		ORDER BY role, name
	`, plantID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return nil, false
	}
	defer rows.Close()

	out := []pages.LoginEmployeeRow{}
	for rows.Next() {
		var id uuid.UUID
		var name, role string
		if err := rows.Scan(&id, &name, &role); err != nil {
			continue
		}
		out = append(out, pages.LoginEmployeeRow{
			ID:        id.String(),
			Name:      name,
			Initials:  initialsOf(name),
			RoleLabel: strings.ToUpper(role[:1]) + role[1:],
			CardClass: "staff-button",
		})
	}
	return out, true
}

func initialsOf(name string) string {
	fields := strings.Fields(name)
	if len(fields) == 0 {
		return "?"
	}
	if len(fields) == 1 {
		if len(fields[0]) == 0 {
			return "?"
		}
		return strings.ToUpper(fields[0][:1])
	}
	first := fields[0][:1]
	last := fields[len(fields)-1][:1]
	return strings.ToUpper(first) + "." + strings.ToUpper(last) + "."
}

// LoginPage renders the shift-start screen: employee picker, PIN pad,
// opening cash count. Public - this is the credential check itself.
func LoginPage(deps *config.Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		plant := middleware.GetPlant(r.Context())
		if plant == nil {
			http.Error(w, "Plant not found", http.StatusNotFound)
			return
		}
		employees, ok := loadEmployees(w, r, deps, plant.ID)
		if !ok {
			return
		}
		data := pages.LoginCardData{Employees: employees}
		if err := pages.StaffTerminalLogin(plant, data).Render(r.Context(), w); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	}
}

// StartShift validates the selected employee's PIN, opens (or resumes)
// their shift, and establishes the terminal session.
func StartShift(deps *config.Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		plant := middleware.GetPlant(r.Context())
		if plant == nil {
			http.Error(w, "Plant not found", http.StatusNotFound)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		sm := deps.SessionManager
		employeeIDStr := r.FormValue("employee_id")
		pin := normalizeDigits(r.FormValue("pin"))
		openingCashStr := normalizeDigits(r.FormValue("opening_cash"))

		employees, ok := loadEmployees(w, r, deps, plant.ID)
		if !ok {
			return
		}

		renderError := func(msg, class string, locked bool) {
			for i := range employees {
				if employees[i].ID == employeeIDStr {
					employees[i].CardClass = "staff-button active"
				}
			}
			data := pages.LoginCardData{Employees: employees, ErrorMsg: msg, ErrorClass: class, Locked: locked}
			if err := pages.LoginCard(plant, data).Render(r.Context(), w); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
			}
		}

		if lockedUntil := sm.GetInt64(r.Context(), sessionLoginLockedUntil); lockedUntil > time.Now().Unix() {
			seconds := int(lockedUntil - time.Now().Unix())
			renderError("Terminal locked. It will reopen automatically in "+strconv.Itoa(seconds)+"s.", "locked", true)
			return
		}

		if employeeIDStr == "" || len(pin) != 4 || openingCashStr == "" {
			renderError("Select your name, enter your 4-digit PIN, and count opening cash.", "wrong", false)
			return
		}

		employeeID, err := uuid.Parse(employeeIDStr)
		if err != nil {
			renderError("Incorrect PIN.", "wrong", false)
			return
		}

		var pinHash *string
		var role string
		err = deps.AppDB.QueryRow(r.Context(), `
			SELECT pin_hash, role FROM users
			WHERE id = $1 AND plant_id = $2 AND active
		`, employeeID, plant.ID).Scan(&pinHash, &role)

		valid := err == nil && pinHash != nil && bcrypt.CompareHashAndPassword([]byte(*pinHash), []byte(pin)) == nil

		if !valid {
			attempts := sm.GetInt(r.Context(), sessionLoginAttempts) + 1
			if attempts >= maxAttempts {
				sm.Put(r.Context(), sessionLoginLockedUntil, time.Now().Add(lockDuration).Unix())
				sm.Put(r.Context(), sessionLoginAttempts, 0)
				renderError("Terminal locked. It will reopen automatically.", "locked", true)
				return
			}
			sm.Put(r.Context(), sessionLoginAttempts, attempts)
			renderError("Incorrect PIN. "+strconv.Itoa(maxAttempts-attempts)+" attempts remaining.", "wrong", false)
			return
		}

		sm.Remove(r.Context(), sessionLoginAttempts)
		sm.Remove(r.Context(), sessionLoginLockedUntil)

		var shiftID uuid.UUID
		err = deps.AppDB.QueryRow(r.Context(), `
			SELECT id FROM shifts WHERE plant_id = $1 AND user_id = $2 AND closed_at IS NULL
		`, plant.ID, employeeID).Scan(&shiftID)
		if err != nil {
			openingCashKobo := int64(0)
			if n, convErr := strconv.Atoi(openingCashStr); convErr == nil {
				openingCashKobo = int64(n) * 100
			}
			err = deps.AppDB.QueryRow(r.Context(), `
				INSERT INTO shifts (plant_id, user_id, opening_cash_kobo)
				VALUES ($1, $2, $3)
				RETURNING id
			`, plant.ID, employeeID, openingCashKobo).Scan(&shiftID)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
		}

		if err := sm.RenewToken(r.Context()); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		sm.Put(r.Context(), middleware.SessionUserIDKey, employeeID.String())
		sm.Put(r.Context(), middleware.SessionPlantIDKey, plant.ID.String())
		sm.Put(r.Context(), middleware.SessionShiftIDKey, shiftID.String())

		w.Header().Set("HX-Redirect", "/terminal/dashboard")
		w.WriteHeader(http.StatusOK)
	}
}

// Logout ends the terminal session without closing the shift (e.g. the
// device is being handed to another staff member mid-shift is not
// supported yet - this is a plain sign-out).
func Logout(deps *config.Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := deps.SessionManager.Destroy(r.Context()); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("HX-Redirect", "/terminal/login")
		w.WriteHeader(http.StatusOK)
	}
}
