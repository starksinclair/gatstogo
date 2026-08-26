package attendant

import (
	"net/http"

	"gatstogo/internal/config"
	"gatstogo/internal/middleware"
	"gatstogo/web/templates/pages"

	"github.com/google/uuid"
)

// CashPreview re-renders the cash-sale form for the selected cylinder
// size, pricing the amount from the plant's current price.
func CashPreview(deps *config.Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		plant := middleware.GetPlant(r.Context())
		user := middleware.GetUser(r.Context())
		shift := middleware.GetShift(r.Context())

		if err := r.ParseForm(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		cylinder := r.FormValue("cylinder")
		phone := r.FormValue("phone")

		cash := buildCashPanel(r.Context(), deps, shift, cylinder, phone, "")
		renderShell(w, r, deps, plant, user, shift, "cash", nil, &cash)
	}
}

// CashSale records a cash purchase (ticket + cash_movements) and jumps
// straight to the authorised-fill card - the code never has to be
// re-typed since the same attendant just generated it.
func CashSale(deps *config.Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		plant := middleware.GetPlant(r.Context())
		user := middleware.GetUser(r.Context())
		shift := middleware.GetShift(r.Context())

		if err := r.ParseForm(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		cylinder := r.FormValue("cylinder")
		phone := normalizeDigits(r.FormValue("phone"))

		cash := buildCashPanel(r.Context(), deps, shift, cylinder, phone, "")
		if cash.Blocked {
			renderShell(w, r, deps, plant, user, shift, "cash", nil, &cash)
			return
		}

		priceID, pricePerKg, err := latestPrice(r.Context(), deps, plant.ID)
		if err != nil || pricePerKg == 0 {
			cash.ErrorMsg = "No price is set for this plant yet. Ask an owner to set today's price first."
			renderShell(w, r, deps, plant, user, shift, "cash", nil, &cash)
			return
		}

		grams := int64(cylinderKg(cylinder) * 1000)
		amount := (grams * pricePerKg) / 1000

		code, err := generateTicketCode(r.Context(), deps, plant.ID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		var phonePtr *string
		if phone != "" {
			phonePtr = &phone
		}

		tx, err := deps.AppDB.Begin(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		defer tx.Rollback(r.Context())

		var ticketID uuid.UUID
		err = tx.QueryRow(r.Context(), `
			INSERT INTO tickets (
				plant_id, reference, code, customer_phone, size_grams,
				rate_per_kg, price_id, amount, channel, status, paid_at,
				sold_by, shift_id
			) VALUES (
				$1, $2, $3, $4, $5, $6, $7, $8, 'cash', 'paid', now(), $9, $10
			) RETURNING id
		`, plant.ID, uuid.New().String(), code, phonePtr, grams, pricePerKg, priceID, amount, user.ID, shift.ID).Scan(&ticketID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		if _, err := tx.Exec(r.Context(), `
			INSERT INTO cash_movements (plant_id, shift_id, kind, amount_kobo, ticket_id, recorded_by)
			VALUES ($1, $2, 'sale', $3, $4, $5)
		`, plant.ID, shift.ID, amount, ticketID, user.ID); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		if _, err := tx.Exec(r.Context(), `
			INSERT INTO activity_log (plant_id, actor_id, action, subject)
			VALUES ($1, $2, 'cash_sale', $3)
		`, plant.ID, user.ID, code); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		if err := tx.Commit(r.Context()); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		fill := pages.FillPanelData{
			Revealed:       true,
			RevealTicketID: ticketID.String(),
			RevealInitials: "CASH SALE",
			RevealKg:       formatKg(grams),
			RevealAmount:   formatNaira(amount),
			RevealMethod:   "Cash",
		}
		renderShell(w, r, deps, plant, user, shift, "fill", &fill, nil)
	}
}
