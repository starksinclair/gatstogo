package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"gatstogo/internal/audit"
	"gatstogo/internal/auth"
	"gatstogo/internal/csrf"
	"gatstogo/internal/middleware"
	"gatstogo/internal/session"
	"gatstogo/internal/shifts"
	"gatstogo/internal/tenantdb"
	"gatstogo/internal/tickets"
	"gatstogo/web/templates/pages"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// These endpoints (besides terminalPageHandler) are JSON, not HTML -- the
// staff terminal is a fetch()-driven single-page UI (see
// web/templates/pages/terminal.templ), unlike the rest of this
// server-rendered app. writeJSON/writeJSONError are this file's
// equivalent of the render calls everywhere else.

func writeJSON(w http.ResponseWriter, status int, payload map[string]any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeJSONError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]any{"error": message})
}

// ---- GET /terminal (the page itself) ----

// terminalInitPayload is embedded in the page as JSON (see
// terminal.templ) for the frontend to bootstrap from -- the real staff
// list and live price, replacing what used to be hardcoded demo data
// baked into the JS itself.
type terminalInitPayload struct {
	PlantName      string                 `json:"plantName"`
	TodayLabel     string                 `json:"todayLabel"`
	Staff          []terminalStaffPayload `json:"staff"`
	CSRFToken      string                 `json:"csrfToken"`
	PricePerKgKobo int64                  `json:"pricePerKgKobo"`
}

type terminalStaffPayload struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Role string `json:"role"`
	// ShiftOpenSince is set only when this staff member already has an
	// open shift at this plant (formatted "3:04 PM") -- picking their
	// tile and starting would resume it (StartOrResume), not open a new
	// one. Empty means no open shift.
	ShiftOpenSince string `json:"shiftOpenSince,omitempty"`
}

func terminalPageHandler(s *Server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		plant := middleware.GetPlant(r.Context())
		if plant == nil {
			renderNotFound(w, r)
			return
		}

		var (
			staff      []shifts.StaffMember
			pricePerKg int64
			openSince  map[uuid.UUID]time.Time
		)
		_ = tenantdb.WithTenant(r.Context(), s.AppDB, plant.ID, func(ctx context.Context, q tenantdb.Querier) error {
			var err error
			if staff, err = shifts.ListStaff(ctx, q, plant.ID); err != nil {
				log.Println("terminal page: list staff failed:", err)
			}
			if _, pricePerKg, err = loadCurrentPrice(ctx, q, plant.ID); err != nil {
				log.Println("terminal page: load price failed:", err)
			}
			if openSince, err = shifts.OpenShiftStarts(ctx, q, plant.ID); err != nil {
				log.Println("terminal page: load open shifts failed:", err)
			}
			return nil // best-effort: an empty staff list or zero price still renders a usable (if unusable-until-fixed) page
		})

		plantName := strings.TrimSpace(plant.Name)
		if plantName == "" {
			plantName = "Gas plant" // same fallback pages.StaffTerminal's own <title>/data-plant uses
		}
		payload := terminalInitPayload{
			PlantName:      plantName,
			TodayLabel:     time.Now().Format("2 Jan 2006"),
			Staff:          make([]terminalStaffPayload, len(staff)),
			CSRFToken:      middleware.EnsurePublicCSRFCookie(w, r),
			PricePerKgKobo: pricePerKg,
		}
		for i, m := range staff {
			payload.Staff[i] = terminalStaffPayload{ID: m.ID.String(), Name: m.Name, Role: m.Role}
			if openedAt, ok := openSince[m.ID]; ok {
				payload.Staff[i].ShiftOpenSince = openedAt.Format("3:04 PM")
			}
		}

		// Passed straight through to templ.JSONScript, which does its own
		// json.Marshal -- previously this pre-marshaled to a string and
		// interpolated it as {initJSON} inside a literal <script> tag,
		// which templ does NOT run Go-expression interpolation inside
		// (script bodies are treated as opaque text, same as any other
		// HTML element's raw text content) -- so the page was actually
		// shipping the eight literal characters "{ initJSON }" to the
		// browser instead of real JSON, and every terminal page load
		// would fail at JSON.parse(...) before the terminal could boot at
		// all. Only caught by rendering this page for real against a
		// live server; the JS itself had only ever been tested with a
		// hand-rolled harness that supplied a mocked init object directly,
		// never through templ's actual renderer.
		if err := pages.StaffTerminal(plant, payload).Render(r.Context(), w); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	}
}

// ---- POST /terminal/pin (public: no staff session exists yet) ----

func terminalPinHandler(s *Server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		plant := middleware.GetPlant(r.Context())
		if plant == nil {
			renderNotFound(w, r)
			return
		}
		if err := r.ParseForm(); err != nil {
			writeJSONError(w, http.StatusBadRequest, "could not read submission")
			return
		}

		userID, err := uuid.Parse(r.FormValue("user_id"))
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "choose your name")
			return
		}
		pin := r.FormValue("pin")
		if pin == "" {
			writeJSONError(w, http.StatusBadRequest, "enter your PIN")
			return
		}
		openingCashNaira, err := strconv.ParseFloat(strings.TrimSpace(r.FormValue("opening_cash")), 64)
		if err != nil || openingCashNaira < 0 {
			writeJSONError(w, http.StatusBadRequest, "enter the opening cash amount")
			return
		}
		openingCashKobo := int64(math.Round(openingCashNaira * 100))
		deviceID := strings.TrimSpace(r.FormValue("device_id"))

		// Checked before touching the database -- a locked-out user
		// shouldn't get a fresh 20-second window just by retrying.
		locked, retryAfter, err := s.PINLimiter.Locked(r.Context(), userID)
		if err != nil {
			log.Println("terminal pin: lock check failed:", err)
			writeJSONError(w, http.StatusInternalServerError, "something went wrong")
			return
		}
		if locked {
			writeJSON(w, http.StatusTooManyRequests, map[string]any{
				"error":               "terminal locked, try again shortly",
				"retry_after_seconds": int(retryAfter.Seconds()),
			})
			return
		}

		var (
			name, role string
			pinHash    *string
			active     bool
			found      bool
		)
		err = tenantdb.WithTenant(r.Context(), s.AppDB, plant.ID, func(ctx context.Context, q tenantdb.Querier) error {
			row := q.QueryRow(ctx, `
				SELECT name, role, pin_hash, active FROM users
				WHERE id = $1 AND plant_id = $2 AND role IN ('manager', 'cashier', 'operator')
			`, userID, plant.ID)
			switch scanErr := row.Scan(&name, &role, &pinHash, &active); scanErr {
			case nil:
				found = true
				return nil
			case pgx.ErrNoRows:
				found = false
				return nil
			default:
				return scanErr
			}
		})
		if err != nil {
			log.Println("terminal pin: lookup failed:", err)
			writeJSONError(w, http.StatusInternalServerError, "something went wrong")
			return
		}

		hash := ""
		if pinHash != nil {
			hash = *pinHash
		}
		if !found || !active || !auth.VerifyPassword(hash, pin) {
			if found {
				if err := s.PINLimiter.RecordFailure(r.Context(), userID); err != nil {
					log.Println("terminal pin: record failure error:", err)
				}
			}
			writeJSONError(w, http.StatusUnauthorized, "incorrect PIN")
			return
		}
		_ = s.PINLimiter.Reset(r.Context(), userID)

		var (
			shift                        *shifts.Shift
			resumed                      bool
			fillsCompleted               int
			gramsDispensed, cashHeldKobo int64
		)
		err = tenantdb.WithTenant(r.Context(), s.AppDB, plant.ID, func(ctx context.Context, q tenantdb.Querier) error {
			sh, wasResumed, err := shifts.StartOrResume(ctx, q, plant.ID, userID, openingCashKobo, deviceID)
			if err != nil {
				return err
			}
			shift, resumed = sh, wasResumed
			if !wasResumed {
				if err := audit.Log(ctx, q, &plant.ID, &userID, "shift.opened", "", map[string]any{"opening_cash_kobo": sh.OpeningCashKobo}); err != nil {
					return err
				}
			}
			fillsCompleted, gramsDispensed, cashHeldKobo, err = shifts.Summary(ctx, q, plant.ID, sh.ID)
			return err
		})
		if err != nil {
			log.Println("terminal pin: start shift failed:", err)
			writeJSONError(w, http.StatusInternalServerError, "could not start shift")
			return
		}

		sessionToken, err := s.Sessions.Create(r.Context(), session.Data{
			UserID:  userID.String(),
			Role:    session.Role(role),
			Name:    name,
			PlantID: plant.ID.String(),
			ShiftID: shift.ID.String(),
		}, session.StaffBackstopTTL)
		if err != nil {
			log.Println("terminal pin: create session failed:", err)
			writeJSONError(w, http.StatusInternalServerError, "something went wrong")
			return
		}
		session.WriteCookie(w, sessionToken, session.StaffBackstopTTL)
		csrfToken, _ := csrf.Token(sessionToken)

		writeJSON(w, http.StatusOK, map[string]any{
			"shift_id":          shift.ID,
			"user_id":           userID,
			"name":              name,
			"role":              role,
			"opened_at":         shift.OpenedAt,
			"opening_cash_kobo": shift.OpeningCashKobo,
			"tokens_issued":     shift.TokensIssued,
			"tokens_returned":   shift.TokensReturned,
			"fills_completed":   fillsCompleted,
			"grams_dispensed":   gramsDispensed,
			"cash_held_kobo":    cashHeldKobo,
			"resumed":           resumed,
			"csrf_token":        csrfToken,
		})
	}
}

// ---- Everything below requires middleware.RequireStaffSession ----

// terminalRedeemHandler looks up a ticket by its 4-digit code without
// exposing the customer's name, phone, or reference -- only initials, the
// same privacy boundary the terminal mockup this replaces was explicit
// about.
func terminalRedeemHandler(s *Server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		plant := middleware.GetPlant(r.Context())
		if plant == nil {
			renderNotFound(w, r)
			return
		}
		if err := r.ParseForm(); err != nil {
			writeJSONError(w, http.StatusBadRequest, "could not read submission")
			return
		}
		code := strings.TrimSpace(r.FormValue("code"))
		if len(code) != 4 {
			writeJSONError(w, http.StatusBadRequest, "enter a 4-digit code")
			return
		}

		var redeemed *tickets.RedeemedTicket
		err := tenantdb.WithTenant(r.Context(), s.AppDB, plant.ID, func(ctx context.Context, q tenantdb.Querier) error {
			t, err := tickets.Redeem(ctx, q, plant.ID, code)
			redeemed = t
			return err
		})
		switch {
		case err == nil:
			writeJSON(w, http.StatusOK, map[string]any{
				"ticket_id": redeemed.ID,
				"initials":  redeemed.Initials,
				"size_kg":   formatKg(int64(redeemed.SizeGrams)),
				"amount":    formatNaira(redeemed.AmountKobo),
				"channel":   humanize(redeemed.Channel, "Payment channel"),
			})
		case errors.Is(err, tickets.ErrCodeAlreadyRedeemed):
			writeJSONError(w, http.StatusConflict, "this code has already been filled")
		case errors.Is(err, tickets.ErrCodeNotFound), errors.Is(err, tickets.ErrCodeNotPayable):
			writeJSONError(w, http.StatusNotFound, "incorrect code")
		default:
			log.Println("terminal redeem: failed:", err)
			writeJSONError(w, http.StatusInternalServerError, "something went wrong")
		}
	}
}

func terminalFillHandler(s *Server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		plant := middleware.GetPlant(r.Context())
		actor := middleware.GetActor(r.Context())
		if plant == nil || actor == nil || actor.ShiftID == nil {
			writeJSONError(w, http.StatusUnauthorized, "not signed in")
			return
		}
		if err := r.ParseForm(); err != nil {
			writeJSONError(w, http.StatusBadRequest, "could not read submission")
			return
		}
		ticketID, err := uuid.Parse(r.FormValue("ticket_id"))
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid ticket")
			return
		}

		var fillsCompleted int
		var gramsDispensed int64
		err = tenantdb.WithTenant(r.Context(), s.AppDB, plant.ID, func(ctx context.Context, q tenantdb.Querier) error {
			if err := tickets.Fill(ctx, q, plant.ID, ticketID, *actor.ShiftID); err != nil {
				return err
			}
			if err := audit.Log(ctx, q, &plant.ID, &actor.UserID, "ticket.filled", ticketID.String(), nil); err != nil {
				return err
			}
			var err error
			fillsCompleted, gramsDispensed, _, err = shifts.Summary(ctx, q, plant.ID, *actor.ShiftID)
			return err
		})
		if err != nil {
			log.Println("terminal fill: failed:", err)
			writeJSONError(w, http.StatusBadRequest, "could not confirm fill")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":              true,
			"fills_completed": fillsCompleted,
			"grams_dispensed": gramsDispensed,
		})
	}
}

func terminalCashSaleHandler(s *Server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		plant := middleware.GetPlant(r.Context())
		actor := middleware.GetActor(r.Context())
		if plant == nil || actor == nil || actor.ShiftID == nil {
			writeJSONError(w, http.StatusUnauthorized, "not signed in")
			return
		}
		if err := r.ParseForm(); err != nil {
			writeJSONError(w, http.StatusBadRequest, "could not read submission")
			return
		}
		cylinderKg, err := strconv.ParseFloat(strings.TrimSpace(r.FormValue("cylinder_kg")), 64)
		if err != nil || cylinderKg <= 0 {
			writeJSONError(w, http.StatusBadRequest, "choose a cylinder size")
			return
		}
		phone := strings.TrimSpace(r.FormValue("phone"))
		sizeGrams := int(math.Round(cylinderKg * 1000))

		var (
			ticket       *tickets.Ticket
			amountKobo   int64
			tokenNumber  int
			tokenIssued  bool
			cashHeldKobo int64
		)
		err = tenantdb.WithTenant(r.Context(), s.AppDB, plant.ID, func(ctx context.Context, q tenantdb.Querier) error {
			priceID, pricePerKg, err := loadCurrentPrice(ctx, q, plant.ID)
			if err != nil {
				return err
			}
			amountKobo = tickets.AmountFromGrams(sizeGrams, pricePerKg)

			// Same tickets.Purchase core the online checkout
			// (buyGasSubmitHandler, cmd/server/tickets.go) goes through --
			// CreatePaidCash is just the 'paid'-immediately,
			// channel='cash' entry point into it.
			t, err := tickets.CreatePaidCash(ctx, q, tickets.PurchaseParams{
				PlantID:       plant.ID,
				PlantSlug:     plant.Slug,
				PriceID:       priceID,
				RatePerKgKobo: pricePerKg,
				AmountKobo:    amountKobo,
				SizeGrams:     sizeGrams,
				CustomerPhone: phone,
			}, actor.UserID, *actor.ShiftID)
			if err != nil {
				return err
			}
			ticket = t

			if _, err := q.Exec(ctx, `
				INSERT INTO cash_movements (plant_id, shift_id, kind, amount_kobo, ticket_id, recorded_by, note)
				VALUES ($1, $2, 'sale', $3, $4, $5, 'Cash sale at terminal')
			`, plant.ID, *actor.ShiftID, amountKobo, t.ID, actor.UserID); err != nil {
				return err
			}

			if actor.Role == session.RoleCashier {
				n, err := shifts.IncrementTokensIssued(ctx, q, plant.ID, *actor.ShiftID)
				if err != nil {
					return err
				}
				tokenNumber, tokenIssued = n, true
			}

			if err := audit.Log(ctx, q, &plant.ID, &actor.UserID, "ticket.cash_sale", ticket.Reference, map[string]any{
				"amount_kobo": amountKobo,
				"size_grams":  sizeGrams,
			}); err != nil {
				return err
			}

			cashHeldKobo, err = shiftCashHeld(ctx, q, plant.ID, *actor.ShiftID)
			return err
		})
		if err != nil {
			log.Println("terminal cash sale: failed:", err)
			writeJSONError(w, http.StatusInternalServerError, "could not record cash sale")
			return
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"ticket_id":      ticket.ID,
			"code":           ticket.Code,
			"amount":         formatNaira(amountKobo),
			"token_issued":   tokenIssued,
			"token_number":   tokenNumber,
			"cash_held_kobo": cashHeldKobo,
		})
	}
}

func shiftCashHeld(ctx context.Context, q tenantdb.Querier, plantID, shiftID uuid.UUID) (int64, error) {
	_, _, cashHeldKobo, err := shifts.Summary(ctx, q, plantID, shiftID)
	return cashHeldKobo, err
}

func terminalTokenReturnHandler(s *Server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		plant := middleware.GetPlant(r.Context())
		actor := middleware.GetActor(r.Context())
		if plant == nil || actor == nil || actor.ShiftID == nil {
			writeJSONError(w, http.StatusUnauthorized, "not signed in")
			return
		}

		var newCount int
		err := tenantdb.WithTenant(r.Context(), s.AppDB, plant.ID, func(ctx context.Context, q tenantdb.Querier) error {
			n, err := shifts.IncrementTokensReturned(ctx, q, plant.ID, *actor.ShiftID)
			newCount = n
			return err
		})
		if err != nil {
			log.Println("terminal token return: failed:", err)
			writeJSONError(w, http.StatusInternalServerError, "could not record token return")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"tokens_returned": newCount})
	}
}

func terminalShiftEndHandler(s *Server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		plant := middleware.GetPlant(r.Context())
		actor := middleware.GetActor(r.Context())
		if plant == nil || actor == nil || actor.ShiftID == nil {
			writeJSONError(w, http.StatusUnauthorized, "not signed in")
			return
		}
		if err := r.ParseForm(); err != nil {
			writeJSONError(w, http.StatusBadRequest, "could not read submission")
			return
		}
		closingCashNaira, err := strconv.ParseFloat(strings.TrimSpace(r.FormValue("closing_cash")), 64)
		if err != nil || closingCashNaira < 0 {
			writeJSONError(w, http.StatusBadRequest, "enter the closing cash amount")
			return
		}
		closingCashKobo := int64(math.Round(closingCashNaira * 100))
		notes := strings.TrimSpace(r.FormValue("notes"))

		err = tenantdb.WithTenant(r.Context(), s.AppDB, plant.ID, func(ctx context.Context, q tenantdb.Querier) error {
			if err := shifts.Close(ctx, q, plant.ID, *actor.ShiftID, actor.UserID, closingCashKobo, notes); err != nil {
				return err
			}
			return audit.Log(ctx, q, &plant.ID, &actor.UserID, "shift.closed", actor.ShiftID.String(), map[string]any{
				"closing_cash_kobo": closingCashKobo,
			})
		})
		if err != nil {
			log.Println("terminal shift end: failed:", err)
			writeJSONError(w, http.StatusInternalServerError, "could not close shift")
			return
		}

		// The session is over the instant the shift is -- delete it
		// outright rather than let it linger until StaffBackstopTTL
		// lapses (see session.StaffBackstopTTL's doc comment).
		_ = s.Sessions.Delete(r.Context(), actor.Token)
		session.ClearCookie(w)

		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}
}
