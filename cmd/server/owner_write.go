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
	"gatstogo/internal/csrf"
	"gatstogo/internal/hours"
	"gatstogo/internal/middleware"
	"gatstogo/internal/prices"
	"gatstogo/internal/pumps"
	"gatstogo/internal/session"
	"gatstogo/internal/staff"
	"gatstogo/internal/tenantdb"
	"gatstogo/internal/uploads"
	"gatstogo/web/templates/pages"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
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
		allowed, err := ownerActorCan(r.Context(), s, plant.ID, actor, "can_edit_price")
		if err != nil {
			log.Println("owner set price: permission lookup failed:", err)
			http.Redirect(w, r, "/owner?error=price-permission#settings", http.StatusSeeOther)
			return
		}
		if !allowed {
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
		allowed, err := ownerActorCan(r.Context(), s, plant.ID, actor, "can_manage_staff")
		if err != nil {
			log.Println("owner create staff: permission lookup failed:", err)
			http.Redirect(w, r, "/owner?error=staff-permission#staff", http.StatusSeeOther)
			return
		}
		if !allowed {
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
		err = tenantdb.WithTenant(r.Context(), s.AppDB, plant.ID, func(ctx context.Context, q tenantdb.Querier) error {
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
		allowed, err := ownerActorCan(r.Context(), s, plant.ID, actor, "can_manage_staff")
		if err != nil {
			log.Println("owner staff status: permission lookup failed:", err)
			http.Redirect(w, r, "/owner?error=staff-permission#staff", http.StatusSeeOther)
			return
		}
		if !allowed {
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
		canManagePumps := r.FormValue("can_manage_pumps") == "true"

		err = tenantdb.WithTenant(r.Context(), s.AppDB, plant.ID, func(ctx context.Context, q tenantdb.Querier) error {
			tag, err := q.Exec(ctx, `
				UPDATE users
				SET can_edit_price = $3,
				    can_manage_staff = $4,
				    can_manage_pumps = $5
				WHERE id = $1
				  AND plant_id = $2
				  AND role = 'manager'
			`, userID, plant.ID, canEditPrice, canManageStaff, canManagePumps)
			if err != nil {
				return err
			}
			if tag.RowsAffected() == 0 {
				return staff.ErrNotFound
			}
			return audit.Log(ctx, q, &plant.ID, &actor.UserID, "staff.permissions_updated", userID.String(), map[string]any{
				"can_edit_price":   canEditPrice,
				"can_manage_staff": canManageStaff,
				"can_manage_pumps": canManagePumps,
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

// ---- POST /owner/hours ----

func ownerHoursHandler(s *Server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		plant := middleware.GetPlant(r.Context())
		actor := middleware.GetActor(r.Context())
		if plant == nil || actor == nil {
			http.Error(w, "Not found", http.StatusNotFound)
			return
		}
		allowed, err := ownerActorCan(r.Context(), s, plant.ID, actor, "can_manage_pumps")
		if err != nil {
			log.Println("owner hours: permission lookup failed:", err)
			http.Redirect(w, r, "/owner?error=pump-permission#operations", http.StatusSeeOther)
			return
		}
		if !allowed {
			http.Redirect(w, r, "/owner?error=pump-permission#operations", http.StatusSeeOther)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Redirect(w, r, "/owner?error=form#operations", http.StatusSeeOther)
			return
		}
		segments := hours.ParseWeekForm(r.FormValue)
		err = tenantdb.WithTenant(r.Context(), s.AppDB, plant.ID, func(ctx context.Context, q tenantdb.Querier) error {
			if err := hours.ReplaceWeek(ctx, q, plant.ID, segments); err != nil {
				return err
			}
			return audit.Log(ctx, q, &plant.ID, &actor.UserID, "plant.hours_updated", plant.ID.String(), map[string]any{"segments": len(segments)})
		})
		if err != nil {
			log.Println("owner hours:", err)
			http.Redirect(w, r, "/owner?error=hours#operations", http.StatusSeeOther)
			return
		}
		http.Redirect(w, r, "/owner#operations", http.StatusSeeOther)
	}
}

// ---- POST /owner/pumps ----

func ownerPumpHandler(s *Server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		plant := middleware.GetPlant(r.Context())
		actor := middleware.GetActor(r.Context())
		if plant == nil || actor == nil {
			http.Error(w, "Not found", http.StatusNotFound)
			return
		}
		allowed, err := ownerActorCan(r.Context(), s, plant.ID, actor, "can_manage_pumps")
		if err != nil {
			log.Println("owner pump: permission lookup failed:", err)
			http.Redirect(w, r, "/owner?error=pump-permission#operations", http.StatusSeeOther)
			return
		}
		if !allowed {
			http.Redirect(w, r, "/owner?error=pump-permission#operations", http.StatusSeeOther)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Redirect(w, r, "/owner?error=form#operations", http.StatusSeeOther)
			return
		}
		var id *uuid.UUID
		if rawID := strings.TrimSpace(r.FormValue("id")); rawID != "" {
			parsed, err := uuid.Parse(rawID)
			if err != nil {
				http.Redirect(w, r, "/owner?error=pump#operations", http.StatusSeeOther)
				return
			}
			id = &parsed
		}
		sortOrder, _ := strconv.Atoi(strings.TrimSpace(r.FormValue("sort_order")))
		name := r.FormValue("name")
		status := r.FormValue("status")

		var pumpID uuid.UUID
		err = tenantdb.WithTenant(r.Context(), s.AppDB, plant.ID, func(ctx context.Context, q tenantdb.Querier) error {
			id, err := pumps.Upsert(ctx, q, plant.ID, id, name, status, sortOrder)
			if err != nil {
				return err
			}
			pumpID = id
			return audit.Log(ctx, q, &plant.ID, &actor.UserID, "pump.updated", pumpID.String(), map[string]any{
				"name":   strings.TrimSpace(name),
				"status": status,
			})
		})
		if err != nil {
			log.Println("owner pump:", err)
			http.Redirect(w, r, "/owner?error=pump#operations", http.StatusSeeOther)
			return
		}
		http.Redirect(w, r, "/owner#operations", http.StatusSeeOther)
	}
}

func ownerActorCan(ctx context.Context, s *Server, plantID uuid.UUID, actor *middleware.Actor, permission string) (bool, error) {
	if actor.Role == session.RoleOwner {
		return true, nil
	}
	if actor.Role != session.RoleManager {
		return false, nil
	}

	var query string
	switch permission {
	case "can_edit_price":
		query = `
			SELECT can_edit_price
			FROM users
			WHERE id = $1
			  AND plant_id = $2
			  AND role = 'manager'
			  AND active = TRUE
		`
	case "can_manage_staff":
		query = `
			SELECT can_manage_staff
			FROM users
			WHERE id = $1
			  AND plant_id = $2
			  AND role = 'manager'
			  AND active = TRUE
		`
	case "can_manage_pumps":
		query = `
			SELECT can_manage_pumps
			FROM users
			WHERE id = $1
			  AND plant_id = $2
			  AND role = 'manager'
			  AND active = TRUE
		`
	default:
		return false, nil
	}

	var allowed bool
	err := tenantdb.WithTenant(ctx, s.AppDB, plantID, func(ctx context.Context, q tenantdb.Querier) error {
		return q.QueryRow(ctx, query, actor.UserID, plantID).Scan(&allowed)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return allowed, err
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
			http.Redirect(w, r, "/owner/password?error=password-form", http.StatusSeeOther)
			return
		}
		currentPassword := r.FormValue("current_password")
		newPassword := r.FormValue("new_password")
		confirm := r.FormValue("new_password_confirm")

		if currentPassword == "" || newPassword == "" || confirm == "" {
			http.Redirect(w, r, "/owner/password?error=password-missing", http.StatusSeeOther)
			return
		}
		if newPassword != confirm {
			http.Redirect(w, r, "/owner/password?error=password-mismatch", http.StatusSeeOther)
			return
		}
		if len(newPassword) < ownerMinPasswordLength {
			http.Redirect(w, r, "/owner/password?error=password-short", http.StatusSeeOther)
			return
		}
		if newPassword == currentPassword {
			http.Redirect(w, r, "/owner/password?error=password-same", http.StatusSeeOther)
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
				http.Redirect(w, r, "/owner/password?error=password-current", http.StatusSeeOther)
				return
			}
			log.Println("owner change password:", err)
			http.Redirect(w, r, "/owner/password?error=password-form", http.StatusSeeOther)
			return
		}
		http.Redirect(w, r, "/owner/password?done=1", http.StatusSeeOther)
	}
}

// ownerMinPasswordLength is the shortest new password the change-password
// page accepts (the page's live checklist shows the same rule).
const ownerMinPasswordLength = 8

// ---- GET /owner/password ----

// ownerChangePasswordPageHandler renders the dedicated change-password
// page (Settings links here). ?error= shows a message from
// ownerErrorMessage; ?done=1 shows the "Password updated" confirmation.
func ownerChangePasswordPageHandler(s *Server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		plant := middleware.GetPlant(r.Context())
		actor := middleware.GetActor(r.Context())
		if plant == nil || actor == nil {
			http.Error(w, "Not found", http.StatusNotFound)
			return
		}
		csrfToken, _ := csrf.Token(actor.Token)
		errorMsg := ownerErrorMessage(r.URL.Query().Get("error"))
		done := r.URL.Query().Get("done") == "1" && errorMsg == ""
		w.Header().Set("Cache-Control", "no-store")
		if err := pages.OwnerChangePassword(plant.Name, errorMsg, done, csrfToken).Render(r.Context(), w); err != nil {
			log.Println("render owner change password:", err)
		}
	}
}
