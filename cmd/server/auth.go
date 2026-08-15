package main

import (
	"context"
	"log"
	"net/http"
	"strings"

	"gatstogo/internal/auth"
	"gatstogo/internal/middleware"
	"gatstogo/internal/session"
	"gatstogo/internal/tenantdb"
	"gatstogo/web/templates/pages"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ---- Owner / manager login (tenant-scoped) ----

func ownerLoginPageHandler(s *Server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		plant := middleware.GetPlant(r.Context())
		if plant == nil {
			renderNotFound(w, r)
			return
		}
		csrfToken := middleware.EnsurePublicCSRFCookie(w, r)
		if err := pages.OwnerLogin(plant.Name, "", csrfToken).Render(r.Context(), w); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	}
}

func ownerLoginSubmitHandler(s *Server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		plant := middleware.GetPlant(r.Context())
		if plant == nil {
			renderNotFound(w, r)
			return
		}
		if err := r.ParseForm(); err != nil {
			renderOwnerLoginError(w, r, plant.Name, "Could not read that submission. Try again.")
			return
		}
		phone := strings.TrimSpace(r.FormValue("phone"))
		password := r.FormValue("password")
		if phone == "" || password == "" {
			renderOwnerLoginError(w, r, plant.Name, "Enter your phone number and password.")
			return
		}

		var (
			userID       uuid.UUID
			name, role   string
			passwordHash *string
			active       bool
			found        bool
		)
		err := tenantdb.WithTenant(r.Context(), s.AppDB, plant.ID, func(ctx context.Context, q tenantdb.Querier) error {
			row := q.QueryRow(ctx, `
				SELECT id, name, role, password_hash, active
				FROM users
				WHERE plant_id = $1 AND phone = $2 AND role IN ('owner', 'manager')
			`, plant.ID, phone)
			switch scanErr := row.Scan(&userID, &name, &role, &passwordHash, &active); scanErr {
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
			log.Println("owner login: query failed:", err)
			renderOwnerLoginError(w, r, plant.Name, "Something went wrong. Try again.")
			return
		}

		hash := ""
		if passwordHash != nil {
			hash = *passwordHash
		}
		if !found || !active || !auth.VerifyPassword(hash, password) {
			renderOwnerLoginError(w, r, plant.Name, "Incorrect phone number or password.")
			return
		}

		token, err := s.Sessions.Create(r.Context(), session.Data{
			UserID:  userID.String(),
			Role:    session.Role(role),
			Name:    name,
			PlantID: plant.ID.String(),
		}, session.OwnerTTL)
		if err != nil {
			log.Println("owner login: create session failed:", err)
			renderOwnerLoginError(w, r, plant.Name, "Something went wrong. Try again.")
			return
		}

		session.WriteCookie(w, token, session.OwnerTTL)
		http.Redirect(w, r, "/owner", http.StatusSeeOther)
	}
}

func renderOwnerLoginError(w http.ResponseWriter, r *http.Request, plantName, message string) {
	csrfToken := middleware.EnsurePublicCSRFCookie(w, r)
	w.WriteHeader(http.StatusUnauthorized)
	if err := pages.OwnerLogin(plantName, message, csrfToken).Render(r.Context(), w); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func ownerLogoutHandler(s *Server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if token, ok := session.ReadCookie(r); ok {
			_ = s.Sessions.Delete(r.Context(), token)
		}
		session.ClearCookie(w)
		http.Redirect(w, r, "/owner/login", http.StatusSeeOther)
	}
}

// ---- Admin login (platform-wide, not tenant-scoped) ----

func adminLoginPageHandler(s *Server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		csrfToken := middleware.EnsurePublicCSRFCookie(w, r)
		if err := pages.AdminLogin("", csrfToken).Render(r.Context(), w); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	}
}

func adminLoginSubmitHandler(s *Server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			renderAdminLoginError(w, r, "Could not read that submission. Try again.")
			return
		}
		phone := strings.TrimSpace(r.FormValue("phone"))
		password := r.FormValue("password")
		if phone == "" || password == "" {
			renderAdminLoginError(w, r, "Enter your phone number and password.")
			return
		}

		// users.phone is only unique per (plant_id, phone), not globally --
		// an admin user's phone could in principle collide with an admin
		// row at a different plant. Rather than assume at most one match,
		// check every 'admin'-role row with this phone and accept the
		// first one whose password actually verifies.
		rows, err := s.AdminDB.Query(r.Context(), `
			SELECT id, name, password_hash, active
			FROM users
			WHERE phone = $1 AND role = 'admin'
			LIMIT 5
		`, phone)
		if err != nil {
			log.Println("admin login: query failed:", err)
			renderAdminLoginError(w, r, "Something went wrong. Try again.")
			return
		}

		var (
			userID  uuid.UUID
			name    string
			matched bool
		)
		for rows.Next() {
			var (
				id           uuid.UUID
				rowName      string
				passwordHash *string
				active       bool
			)
			if scanErr := rows.Scan(&id, &rowName, &passwordHash, &active); scanErr != nil {
				continue
			}
			hash := ""
			if passwordHash != nil {
				hash = *passwordHash
			}
			if active && auth.VerifyPassword(hash, password) {
				userID, name, matched = id, rowName, true
				break
			}
		}
		rows.Close()

		if !matched {
			renderAdminLoginError(w, r, "Incorrect phone number or password.")
			return
		}

		token, err := s.Sessions.Create(r.Context(), session.Data{
			UserID: userID.String(),
			Role:   session.RoleAdmin,
			Name:   name,
			// PlantID intentionally left empty: admin sessions are
			// platform-wide, not scoped to whichever plant this admin
			// user's row happens to live under (see session.Data doc).
		}, session.OwnerTTL)
		if err != nil {
			log.Println("admin login: create session failed:", err)
			renderAdminLoginError(w, r, "Something went wrong. Try again.")
			return
		}

		session.WriteCookie(w, token, session.OwnerTTL)
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
	}
}

func renderAdminLoginError(w http.ResponseWriter, r *http.Request, message string) {
	csrfToken := middleware.EnsurePublicCSRFCookie(w, r)
	w.WriteHeader(http.StatusUnauthorized)
	if err := pages.AdminLogin(message, csrfToken).Render(r.Context(), w); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func adminLogoutHandler(s *Server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if token, ok := session.ReadCookie(r); ok {
			_ = s.Sessions.Delete(r.Context(), token)
		}
		session.ClearCookie(w)
		http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
	}
}
