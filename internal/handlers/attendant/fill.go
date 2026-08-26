package attendant

import (
	"net/http"
	"strconv"
	"time"

	"gatstogo/internal/config"
	"gatstogo/internal/middleware"
	"gatstogo/web/templates/pages"

	"github.com/google/uuid"
)

func methodLabel(channel string) string {
	switch channel {
	case "transfer":
		return "Bank transfer"
	case "ussd":
		return "USSD"
	case "terminal":
		return "POS terminal"
	case "cash":
		return "Cash"
	default:
		return channel
	}
}

// FillTicket validates a 4-digit customer code and, if it belongs to a
// paid, unfilled ticket for this plant, reveals the authorised-fill card.
func FillTicket(deps *config.Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		plant := middleware.GetPlant(r.Context())
		user := middleware.GetUser(r.Context())
		shift := middleware.GetShift(r.Context())
		sm := deps.SessionManager

		if err := r.ParseForm(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		code := normalizeDigits(r.FormValue("code"))

		if lockedUntil := sm.GetInt64(r.Context(), sessionFillLockedUntil); lockedUntil > time.Now().Unix() {
			seconds := int(lockedUntil - time.Now().Unix())
			fill := buildFillPanel(true, seconds, "", "")
			renderShell(w, r, deps, plant, user, shift, "fill", &fill, nil)
			return
		}

		if len(code) != 4 {
			fill := buildFillPanel(false, 0, "Enter a 4-digit code.", "wrong")
			renderShell(w, r, deps, plant, user, shift, "fill", &fill, nil)
			return
		}

		var ticketID uuid.UUID
		var customerName *string
		var sizeGrams int
		var amount int64
		var channel, status string
		err := deps.AppDB.QueryRow(r.Context(), `
			SELECT id, customer_name, size_grams, amount, channel, status
			FROM tickets
			WHERE plant_id = $1 AND code = $2
			ORDER BY created_at DESC
			LIMIT 1
		`, plant.ID, code).Scan(&ticketID, &customerName, &sizeGrams, &amount, &channel, &status)

		if err == nil && (status == "filled" || status == "voided") {
			fill := buildFillPanel(false, 0, "This code has already been used today.", "used")
			renderShell(w, r, deps, plant, user, shift, "fill", &fill, nil)
			return
		}

		if err != nil || status != "paid" {
			attempts := sm.GetInt(r.Context(), sessionFillAttempts) + 1
			if attempts >= maxAttempts {
				sm.Put(r.Context(), sessionFillLockedUntil, time.Now().Add(lockDuration).Unix())
				sm.Put(r.Context(), sessionFillAttempts, 0)
				fill := buildFillPanel(true, int(lockDuration.Seconds()), "", "")
				renderShell(w, r, deps, plant, user, shift, "fill", &fill, nil)
				return
			}
			sm.Put(r.Context(), sessionFillAttempts, attempts)
			fill := buildFillPanel(false, 0, "Incorrect code. "+strconv.Itoa(maxAttempts-attempts)+" attempts remaining.", "wrong")
			renderShell(w, r, deps, plant, user, shift, "fill", &fill, nil)
			return
		}

		sm.Remove(r.Context(), sessionFillAttempts)
		sm.Remove(r.Context(), sessionFillLockedUntil)

		initials := "—"
		if customerName != nil {
			initials = initialsOf(*customerName)
		}
		fill := pages.FillPanelData{
			Revealed:       true,
			RevealTicketID: ticketID.String(),
			RevealInitials: initials,
			RevealKg:       formatKg(int64(sizeGrams)),
			RevealAmount:   formatNaira(amount),
			RevealMethod:   methodLabel(channel),
		}
		renderShell(w, r, deps, plant, user, shift, "fill", &fill, nil)
	}
}

// ConfirmFill marks the authorised ticket as filled and logs the action.
func ConfirmFill(deps *config.Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		plant := middleware.GetPlant(r.Context())
		user := middleware.GetUser(r.Context())
		shift := middleware.GetShift(r.Context())

		if err := r.ParseForm(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		ticketID, err := uuid.Parse(r.FormValue("ticket_id"))
		if err != nil {
			http.Error(w, "invalid ticket", http.StatusBadRequest)
			return
		}

		tx, err := deps.AppDB.Begin(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		defer tx.Rollback(r.Context())

		tag, err := tx.Exec(r.Context(), `
			UPDATE tickets
			SET status = 'filled', filled_at = now(), filled_shift_id = $1,
			    net_grams = size_grams
			WHERE id = $2 AND plant_id = $3 AND status = 'paid'
		`, shift.ID, ticketID, plant.ID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if tag.RowsAffected() == 0 {
			// Already filled/voided by another device since it was revealed.
			fill := buildFillPanel(false, 0, "This code has already been used today.", "used")
			_ = tx.Rollback(r.Context())
			renderShell(w, r, deps, plant, user, shift, "fill", &fill, nil)
			return
		}

		if _, err := tx.Exec(r.Context(), `
			INSERT INTO activity_log (plant_id, actor_id, action, subject)
			SELECT $1, $2, 'ticket_filled', code FROM tickets WHERE id = $3
		`, plant.ID, user.ID, ticketID); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		if err := tx.Commit(r.Context()); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		renderShell(w, r, deps, plant, user, shift, "fill", nil, nil)
	}
}
