package main

import (
	"context"
	"errors"
	"log"
	"math"
	"net/http"
	"strconv"
	"strings"

	"gatstogo/internal/audit"
	"gatstogo/internal/auth"
	"gatstogo/internal/middleware"
	"gatstogo/internal/prices"
	"gatstogo/internal/shifts"
	"gatstogo/internal/staff"
	"gatstogo/internal/tenantdb"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// The owner dashboard is server-rendered, not a JSON API like the
// terminal -- every handler in this file follows the same shape as
// auth.go's login handlers: parse a real <form method="post">, do the
// write inside tenantdb.WithTenant, then redirect back to /owner (with
// #<tab> so the owner lands back where they were -- see the hash-restore
// script in owner_admin.templ's opsShell -- and ?error=<code> on
// failure, mapped to a message by ownerErrorMessage in main.go).

// ---- POST /owner/prices ----

func ownerSetPriceHandler(s *Server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		plant := middleware.GetPlant(r.Context())
		actor := middleware.GetActor(r.Context())
		if plant == nil || actor == nil {
			http.Error(w, "Not found", http.StatusNotFound)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Redirect(w, r, "/owner?error=form#prices", http.StatusSeeOther)
			return
		}
		priceNaira, err := strconv.ParseFloat(strings.TrimSpace(r.FormValue("price_per_kg")), 64)
		if err != nil || priceNaira <= 0 {
			http.Redirect(w, r, "/owner?error=price#prices", http.StatusSeeOther)
			return
		}
		priceKobo := int64(math.Round(priceNaira * 100))

		err = tenantdb.WithTenant(r.Context(), s.AppDB, plant.ID, func(ctx context.Context, q tenantdb.Querier) error {
			priceID, err := prices.Set(ctx, q, plant.ID, priceKobo, actor.UserID)
			if err != nil {
				return err
			}
			return audit.Log(ctx, q, &plant.ID, &actor.UserID, "price.set", priceID.String(), map[string]any{"price_per_kg_kobo": priceKobo})
		})
		if err != nil {
			log.Println("owner set price: failed:", err)
			http.Redirect(w, r, "/owner?error=price#prices", http.StatusSeeOther)
			return
		}
		http.Redirect(w, r, "/owner#prices", http.StatusSeeOther)
	}
}

// ---- POST /owner/staff ----

func ownerCreateStaffHandler(s *Server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		plant := middleware.GetPlant(r.Context())
		actor := middleware.GetActor(r.Context())
		if plant == nil || actor == nil {
			http.Error(w, "Not found", http.StatusNotFound)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Redirect(w, r, "/owner?error=form#staff", http.StatusSeeOther)
			return
		}

		params := staff.CreateParams{
			Name:  r.FormValue("name"),
			Phone: r.FormValue("phone"),
			Role:  r.FormValue("role"),
			PIN:   r.FormValue("pin"),
		}

		var newID uuid.UUID
		err := tenantdb.WithTenant(r.Context(), s.AppDB, plant.ID, func(ctx context.Context, q tenantdb.Querier) error {
			id, err := staff.Create(ctx, q, plant.ID, params)
			if err != nil {
				return err
			}
			newID = id
			return audit.Log(ctx, q, &plant.ID, &actor.UserID, "staff.created", newID.String(), map[string]any{"role": params.Role})
		})
		if err != nil {
			// staff.Create's validation errors (missing field, bad role,
			// bad PIN, phone taken) are all expected user-input mistakes,
			// not server failures -- logged at a lower severity than an
			// unexpected DB error would be, but still surfaced to the
			// owner via the same ?error=staff banner either way.
			log.Println("owner create staff:", err)
			http.Redirect(w, r, "/owner?error=staff#staff", http.StatusSeeOther)
			return
		}
		http.Redirect(w, r, "/owner#staff", http.StatusSeeOther)
	}
}

// ---- POST /owner/staff/{id}/status ----

func ownerStaffStatusHandler(s *Server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		plant := middleware.GetPlant(r.Context())
		actor := middleware.GetActor(r.Context())
		if plant == nil || actor == nil {
			http.Error(w, "Not found", http.StatusNotFound)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Redirect(w, r, "/owner?error=form#staff", http.StatusSeeOther)
			return
		}
		userID, err := uuid.Parse(chi.URLParam(r, "id"))
		if err != nil {
			http.Redirect(w, r, "/owner?error=staff-status#staff", http.StatusSeeOther)
			return
		}
		active := r.FormValue("active") == "true"

		err = tenantdb.WithTenant(r.Context(), s.AppDB, plant.ID, func(ctx context.Context, q tenantdb.Querier) error {
			if err := staff.SetActive(ctx, q, plant.ID, userID, active); err != nil {
				return err
			}
			action := "staff.deactivated"
			if active {
				action = "staff.activated"
			}
			return audit.Log(ctx, q, &plant.ID, &actor.UserID, action, userID.String(), nil)
		})
		if err != nil {
			log.Println("owner staff status:", err)
			http.Redirect(w, r, "/owner?error=staff-status#staff", http.StatusSeeOther)
			return
		}
		http.Redirect(w, r, "/owner#staff", http.StatusSeeOther)
	}
}

// ---- POST /owner/cash-movements ----

func ownerCashMovementHandler(s *Server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		plant := middleware.GetPlant(r.Context())
		actor := middleware.GetActor(r.Context())
		if plant == nil || actor == nil {
			http.Error(w, "Not found", http.StatusNotFound)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Redirect(w, r, "/owner?error=form#cash", http.StatusSeeOther)
			return
		}
		countedNaira, err := strconv.ParseFloat(strings.TrimSpace(r.FormValue("amount")), 64)
		if err != nil || countedNaira < 0 {
			http.Redirect(w, r, "/owner?error=cash#cash", http.StatusSeeOther)
			return
		}
		countedKobo := int64(math.Round(countedNaira * 100))
		kind := r.FormValue("kind")
		note := strings.TrimSpace(r.FormValue("note"))

		// amount_kobo's sign follows the column's own convention
		// (positive = in, negative = out): a deposit or payout both
		// remove physical cash from the drawer (to the bank, or to pay
		// for something), so both go in negative. A 'count' entry is a
		// snapshot, not a flow -- amount stays 0, counted_kobo carries the
		// actual counted figure for reconciliation against what was
		// expected.
		var amountKobo int64
		var countedPtr *int64
		switch kind {
		case "deposit", "payout":
			amountKobo = -countedKobo
		case "count":
			countedPtr = &countedKobo
		default:
			http.Redirect(w, r, "/owner?error=cash#cash", http.StatusSeeOther)
			return
		}

		err = tenantdb.WithTenant(r.Context(), s.AppDB, plant.ID, func(ctx context.Context, q tenantdb.Querier) error {
			shiftID, err := shifts.MostRecentOpenShiftID(ctx, q, plant.ID)
			if err != nil {
				return err
			}
			if err := shifts.RecordCashMovement(ctx, q, plant.ID, shiftID, kind, amountKobo, countedPtr, actor.UserID, note); err != nil {
				return err
			}
			return audit.Log(ctx, q, &plant.ID, &actor.UserID, "cash.recorded", kind, map[string]any{
				"amount_kobo": amountKobo,
			})
		})
		if err != nil {
			log.Println("owner cash movement:", err)
			http.Redirect(w, r, "/owner?error=cash#cash", http.StatusSeeOther)
			return
		}
		http.Redirect(w, r, "/owner#cash", http.StatusSeeOther)
	}
}

// ---- POST /owner/password ----

// errCurrentPasswordWrong distinguishes "the current password didn't
// verify" from every other failure inside the tenantdb.WithTenant
// closure below (a genuine DB error), so the redirect can show the one
// specific, actionable message ("that's not your current password")
// instead of a generic "something went wrong" for a mistake the owner
// can immediately fix.
var errCurrentPasswordWrong = errors.New("owner: current password is incorrect")

// ownerChangePasswordHandler lets an owner who's already logged in (and
// therefore already knows their current password -- an admin-set
// temporary one, or a previous one they chose) set a new one. This is
// deliberately separate from the forgot/reset-password flow
// (cmd/server/auth.go), which is for the opposite case: an owner who
// can't log in at all.
func ownerChangePasswordHandler(s *Server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		plant := middleware.GetPlant(r.Context())
		actor := middleware.GetActor(r.Context())
		if plant == nil || actor == nil {
			http.Error(w, "Not found", http.StatusNotFound)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Redirect(w, r, "/owner?error=password-form#account", http.StatusSeeOther)
			return
		}
		currentPassword := r.FormValue("current_password")
		newPassword := r.FormValue("new_password")
		confirm := r.FormValue("new_password_confirm")

		if currentPassword == "" || newPassword == "" || confirm == "" {
			http.Redirect(w, r, "/owner?error=password-missing#account", http.StatusSeeOther)
			return
		}
		if newPassword != confirm {
			http.Redirect(w, r, "/owner?error=password-mismatch#account", http.StatusSeeOther)
			return
		}

		err := tenantdb.WithTenant(r.Context(), s.AppDB, plant.ID, func(ctx context.Context, q tenantdb.Querier) error {
			var currentHash *string
			if err := q.QueryRow(ctx, `SELECT password_hash FROM users WHERE id = $1 AND plant_id = $2`, actor.UserID, plant.ID).Scan(&currentHash); err != nil {
				return err
			}
			hash := ""
			if currentHash != nil {
				hash = *currentHash
			}
			if !auth.VerifyPassword(hash, currentPassword) {
				return errCurrentPasswordWrong
			}

			newHash, err := auth.HashPassword(newPassword)
			if err != nil {
				return err
			}
			if _, err := q.Exec(ctx, `UPDATE users SET password_hash = $1 WHERE id = $2 AND plant_id = $3`, newHash, actor.UserID, plant.ID); err != nil {
				return err
			}
			return audit.Log(ctx, q, &plant.ID, &actor.UserID, "owner.password_changed", "", nil)
		})
		if err != nil {
			if errors.Is(err, errCurrentPasswordWrong) {
				http.Redirect(w, r, "/owner?error=password-current#account", http.StatusSeeOther)
				return
			}
			log.Println("owner change password:", err)
			http.Redirect(w, r, "/owner?error=password-form#account", http.StatusSeeOther)
			return
		}
		http.Redirect(w, r, "/owner#account", http.StatusSeeOther)
	}
}
