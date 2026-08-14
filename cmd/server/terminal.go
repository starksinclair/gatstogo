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

	"gatstogo/internal/audit"
	"gatstogo/internal/auth"
	"gatstogo/internal/csrf"
	"gatstogo/internal/middleware"
	"gatstogo/internal/session"
	"gatstogo/internal/shifts"
	"gatstogo/internal/tenantdb"
	"gatstogo/internal/tickets"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// These endpoints are JSON, not HTML -- the staff terminal is a
// fetch()-driven single-page UI (see web/templates/pages/terminal.templ
// and its eventual rewiring), unlike the rest of this server-rendered
// app. writeJSON/writeJSONError are this file's equivalent of the render
// calls everywhere else.

func writeJSON(w http.ResponseWriter, status int, payload map[string]any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeJSONError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]any{"error": message})
}

// ---- POST /terminal/pin (public: no staff session exists yet) ----

func terminalPinHandler(s *Server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		plant := middleware.GetPlant(r.Context())
		if plant == nil {
			http.Error(w, "Plant not found", http.StatusNotFound)
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
			shift   *shifts.Shift
			resumed bool
		)
		err = tenantdb.WithTenant(r.Context(), s.AppDB, plant.ID, func(ctx context.Context, q tenantdb.Querier) error {
			sh, wasResumed, err := shifts.StartOrResume(ctx, q, plant.ID, userID, openingCashKobo, deviceID)
			if err != nil {
				return err
			}
			shift, resumed = sh, wasResumed
			if !wasResumed {
				return audit.Log(ctx, q, &plant.ID, &userID, "shift.opened", "", map[string]any{"opening_cash_kobo": sh.OpeningCashKobo})
			}
			return nil
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
			http.Error(w, "Plant not found", http.StatusNotFound)
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

		err = tenantdb.WithTenant(r.Context(), s.AppDB, plant.ID, func(ctx context.Context, q tenantdb.Querier) error {
			if err := tickets.Fill(ctx, q, plant.ID, ticketID, *actor.ShiftID); err != nil {
				return err
			}
			return audit.Log(ctx, q, &plant.ID, &actor.UserID, "ticket.filled", ticketID.String(), nil)
		})
		if err != nil {
			log.Println("terminal fill: failed:", err)
			writeJSONError(w, http.StatusBadRequest, "could not confirm fill")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
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
			ticket      *tickets.Ticket
			amountKobo  int64
			tokenNumber int
			tokenIssued bool
		)
		err = tenantdb.WithTenant(r.Context(), s.AppDB, plant.ID, func(ctx context.Context, q tenantdb.Querier) error {
			priceID, pricePerKg, err := loadCurrentPrice(ctx, q, plant.ID)
			if err != nil {
				return err
			}
			amountKobo = int64(math.Round(float64(sizeGrams) * float64(pricePerKg) / 1000))

			t, err := tickets.CreatePaidCash(ctx, q, tickets.CreatePaidCashParams{
				PlantID:       plant.ID,
				PlantSlug:     plant.Slug,
				PriceID:       priceID,
				RatePerKgKobo: pricePerKg,
				AmountKobo:    amountKobo,
				SizeGrams:     sizeGrams,
				CustomerPhone: phone,
				SoldBy:        actor.UserID,
				ShiftID:       *actor.ShiftID,
			})
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

			return audit.Log(ctx, q, &plant.ID, &actor.UserID, "ticket.cash_sale", ticket.Reference, map[string]any{
				"amount_kobo": amountKobo,
				"size_grams":  sizeGrams,
			})
		})
		if err != nil {
			log.Println("terminal cash sale: failed:", err)
			writeJSONError(w, http.StatusInternalServerError, "could not record cash sale")
			return
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"ticket_id":    ticket.ID,
			"code":         ticket.Code,
			"amount":       formatNaira(amountKobo),
			"token_issued": tokenIssued,
			"token_number": tokenNumber,
		})
	}
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
