package main

import (
	"context"
	"errors"
	"log"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"gatstogo/internal/audit"
	"gatstogo/internal/auth"
	"gatstogo/internal/middleware"
	"gatstogo/internal/prices"
	"gatstogo/internal/session"
	"gatstogo/internal/shifts"
	"gatstogo/internal/staff"
	"gatstogo/internal/tenantdb"
	"gatstogo/internal/uploads"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

var ownerHexColorPattern = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

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
		if actor.Role != session.RoleOwner && !actor.CanEditPrice {
			http.Redirect(w, r, "/owner?error=price-permission#settings", http.StatusSeeOther)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Redirect(w, r, "/owner?error=form#settings", http.StatusSeeOther)
			return
		}
		priceNaira, err := strconv.ParseFloat(strings.TrimSpace(r.FormValue("price_per_kg")), 64)
		if err != nil || priceNaira <= 0 {
			http.Redirect(w, r, "/owner?error=price#settings", http.StatusSeeOther)
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
			http.Redirect(w, r, "/owner?error=price#settings", http.StatusSeeOther)
			return
		}
		http.Redirect(w, r, "/owner#settings", http.StatusSeeOther)
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
		if actor.Role != session.RoleOwner && !actor.CanManageStaff {
			http.Redirect(w, r, "/owner?error=staff-permission#staff", http.StatusSeeOther)
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
			code := "staff"
			if errors.Is(err, staff.ErrPINTaken) {
				code = "staff-pin"
			}
			http.Redirect(w, r, "/owner?error="+code+"#staff", http.StatusSeeOther)
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
		if actor.Role != session.RoleOwner && !actor.CanManageStaff {
			http.Redirect(w, r, "/owner?error=staff-permission#staff", http.StatusSeeOther)
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
		if actor.UserID == userID {
			http.Redirect(w, r, "/owner?error=staff-self#staff", http.StatusSeeOther)
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

// ---- POST /owner/staff/{id}/permissions ----

func ownerStaffPermissionsHandler(s *Server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		plant := middleware.GetPlant(r.Context())
		actor := middleware.GetActor(r.Context())
		if plant == nil || actor == nil {
			http.Error(w, "Not found", http.StatusNotFound)
			return
		}
		if actor.Role != session.RoleOwner {
			http.Redirect(w, r, "/owner?error=staff-permission#staff", http.StatusSeeOther)
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
		canEditPrice := r.FormValue("can_edit_price") == "true"
		canManageStaff := r.FormValue("can_manage_staff") == "true"

		err = tenantdb.WithTenant(r.Context(), s.AppDB, plant.ID, func(ctx context.Context, q tenantdb.Querier) error {
			tag, err := q.Exec(ctx, `
				UPDATE users
				SET can_edit_price = $3,
				    can_manage_staff = $4
				WHERE id = $1
				  AND plant_id = $2
				  AND role = 'manager'
			`, userID, plant.ID, canEditPrice, canManageStaff)
			if err != nil {
				return err
			}
			if tag.RowsAffected() == 0 {
				return staff.ErrNotFound
			}
			return audit.Log(ctx, q, &plant.ID, &actor.UserID, "staff.permissions_updated", userID.String(), map[string]any{
				"can_edit_price":   canEditPrice,
				"can_manage_staff": canManageStaff,
			})
		})
		if err != nil {
			log.Println("owner staff permissions:", err)
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
			http.Redirect(w, r, "/owner?error=form#operations", http.StatusSeeOther)
			return
		}
		countedNaira, err := strconv.ParseFloat(strings.TrimSpace(r.FormValue("amount")), 64)
		if err != nil || countedNaira < 0 {
			http.Redirect(w, r, "/owner?error=cash#operations", http.StatusSeeOther)
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
			http.Redirect(w, r, "/owner?error=cash#operations", http.StatusSeeOther)
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
			http.Redirect(w, r, "/owner?error=cash#operations", http.StatusSeeOther)
			return
		}
		http.Redirect(w, r, "/owner#operations", http.StatusSeeOther)
	}
}

// ---- POST /owner/settings ----

func ownerUpdateSettingsHandler(s *Server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		plant := middleware.GetPlant(r.Context())
		actor := middleware.GetActor(r.Context())
		if plant == nil || actor == nil {
			http.Error(w, "Not found", http.StatusNotFound)
			return
		}
		if err := r.ParseMultipartForm(uploads.MaxLogoBytes + 1<<20); err != nil {
			http.Redirect(w, r, "/owner?error=form#settings", http.StatusSeeOther)
			return
		}

		name := strings.TrimSpace(r.FormValue("name"))
		if name == "" {
			http.Redirect(w, r, "/owner?error=settings#settings", http.StatusSeeOther)
			return
		}

		primaryColor, ok := ownerHexColorOrDefault(r.FormValue("primary_color"), "#006B4D")
		if !ok {
			http.Redirect(w, r, "/owner?error=color#settings", http.StatusSeeOther)
			return
		}
		buttonColor, ok := ownerHexColorOrDefault(r.FormValue("button_color"), primaryColor)
		if !ok {
			http.Redirect(w, r, "/owner?error=color#settings", http.StatusSeeOther)
			return
		}
		secondaryColor, ok := ownerHexColorOrDefault(r.FormValue("secondary_color"), "#10B981")
		if !ok {
			http.Redirect(w, r, "/owner?error=color#settings", http.StatusSeeOther)
			return
		}
		buttonTextColor, ok := ownerHexColorOrDefault(r.FormValue("button_text_color"), "#F8FAFC")
		if !ok {
			http.Redirect(w, r, "/owner?error=color#settings", http.StatusSeeOther)
			return
		}

		logoPath := ""
		logoFile, logoHeader := extractOptionalLogo(r)
		if logoFile != nil {
			defer logoFile.Close()
			savedPath, err := uploads.SaveLogo(plant.Slug, logoFile, logoHeader)
			if err != nil {
				log.Println("owner update settings: logo:", err)
				http.Redirect(w, r, "/owner?error=logo#settings", http.StatusSeeOther)
				return
			}
			logoPath = savedPath
		}

		city := strings.TrimSpace(r.FormValue("city"))
		address := strings.TrimSpace(r.FormValue("address"))
		phone := strings.TrimSpace(r.FormValue("phone"))

		err := tenantdb.WithTenant(r.Context(), s.AppDB, plant.ID, func(ctx context.Context, q tenantdb.Querier) error {
			if _, err := q.Exec(ctx, `
				UPDATE plants
				SET name = $2,
				    city = NULLIF($3, ''),
				    address = NULLIF($4, ''),
				    phone = NULLIF($5, ''),
				    primary_color = $6,
				    button_color = $7,
				    secondary_color = $8,
				    button_text_color = $9,
				    logo_path = COALESCE(NULLIF($10, ''), logo_path)
				WHERE id = $1
			`, plant.ID, name, city, address, phone, primaryColor, buttonColor, secondaryColor, buttonTextColor, logoPath); err != nil {
				return err
			}
			return audit.Log(ctx, q, &plant.ID, &actor.UserID, "plant.settings_updated", plant.ID.String(), map[string]any{
				"logo_updated": logoPath != "",
			})
		})
		if err != nil {
			log.Println("owner update settings:", err)
			http.Redirect(w, r, "/owner?error=settings#settings", http.StatusSeeOther)
			return
		}
		http.Redirect(w, r, "/owner#settings", http.StatusSeeOther)
	}
}

func ownerHexColorOrDefault(value, fallback string) (string, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback, true
	}
	if !strings.HasPrefix(value, "#") {
		value = "#" + value
	}
	if !ownerHexColorPattern.MatchString(value) {
		return "", false
	}
	return strings.ToUpper(value), true
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
			http.Redirect(w, r, "/owner?error=password-form#settings", http.StatusSeeOther)
			return
		}
		currentPassword := r.FormValue("current_password")
		newPassword := r.FormValue("new_password")
		confirm := r.FormValue("new_password_confirm")

		if currentPassword == "" || newPassword == "" || confirm == "" {
			http.Redirect(w, r, "/owner?error=password-missing#settings", http.StatusSeeOther)
			return
		}
		if newPassword != confirm {
			http.Redirect(w, r, "/owner?error=password-mismatch#settings", http.StatusSeeOther)
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
				http.Redirect(w, r, "/owner?error=password-current#settings", http.StatusSeeOther)
				return
			}
			log.Println("owner change password:", err)
			http.Redirect(w, r, "/owner?error=password-form#settings", http.StatusSeeOther)
			return
		}
		http.Redirect(w, r, "/owner#settings", http.StatusSeeOther)
	}
}
