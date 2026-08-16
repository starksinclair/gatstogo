package main

import (
	"context"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"gatstogo/internal/audit"
	"gatstogo/internal/auth"
	"gatstogo/internal/middleware"
	"gatstogo/internal/passwordreset"
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

// ---- Owner forgot/reset password (tenant-scoped, public -- for an owner
// who's actually locked out, can't log in at all) ----

const (
	forgotPasswordRateLimitMax    = 5
	forgotPasswordRateLimitWindow = 15 * time.Minute
)

func ownerForgotPasswordPageHandler(s *Server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		plant := middleware.GetPlant(r.Context())
		if plant == nil {
			renderNotFound(w, r)
			return
		}
		csrfToken := middleware.EnsurePublicCSRFCookie(w, r)
		submitted := r.URL.Query().Get("submitted") == "1"
		errorMsg := ownerForgotPasswordErrorMessage(r.URL.Query().Get("error"))
		if err := pages.OwnerForgotPassword(plant.Name, errorMsg, submitted, csrfToken).Render(r.Context(), w); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	}
}

// ownerForgotPasswordSubmitHandler always redirects to the exact same
// "check your email" confirmation, whether or not the phone number
// actually matched an account -- a form that behaved differently for
// "no such account" would let anyone use it to enumerate which phone
// numbers are registered owners, real information this endpoint has no
// business revealing to an unauthenticated visitor. Rate-limited
// (checkIPRateLimit, cmd/server/accounts.go) for the same reason GET
// /accounts/resolve is: repeatedly probing this endpoint is the abuse
// case, not a rare mistake.
func ownerForgotPasswordSubmitHandler(s *Server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		plant := middleware.GetPlant(r.Context())
		if plant == nil {
			renderNotFound(w, r)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Redirect(w, r, "/owner/forgot-password?error=form", http.StatusSeeOther)
			return
		}

		allowed, err := checkIPRateLimit(r.Context(), s.Redis, "forgot_password_rl:", clientIP(r), forgotPasswordRateLimitMax, forgotPasswordRateLimitWindow)
		if err != nil {
			log.Println("owner forgot password: rate limit check failed:", err)
		} else if !allowed {
			http.Redirect(w, r, "/owner/forgot-password?error=rate-limit", http.StatusSeeOther)
			return
		}

		phone := strings.TrimSpace(r.FormValue("phone"))
		if phone == "" {
			http.Redirect(w, r, "/owner/forgot-password?error=missing", http.StatusSeeOther)
			return
		}

		var (
			userID     uuid.UUID
			ownerEmail string
		)
		found := false
		err = tenantdb.WithTenant(r.Context(), s.AppDB, plant.ID, func(ctx context.Context, q tenantdb.Querier) error {
			var emailPtr *string
			row := q.QueryRow(ctx, `
				SELECT id, email FROM users
				WHERE plant_id = $1 AND phone = $2 AND role IN ('owner', 'manager') AND active
			`, plant.ID, phone)
			switch scanErr := row.Scan(&userID, &emailPtr); scanErr {
			case nil:
				if emailPtr != nil {
					ownerEmail = strings.TrimSpace(*emailPtr)
				}
				found = ownerEmail != ""
				return nil
			case pgx.ErrNoRows:
				return nil
			default:
				return scanErr
			}
		})
		if err != nil {
			// Still the generic confirmation below -- a DB error here
			// shouldn't be a distinguishable signal from "no such
			// account" either.
			log.Println("owner forgot password: lookup failed:", err)
			http.Redirect(w, r, "/owner/forgot-password?submitted=1", http.StatusSeeOther)
			return
		}

		if found {
			token, err := passwordreset.IssueToken(r.Context(), s.Redis, userID)
			if err != nil {
				log.Println("owner forgot password: issue token failed:", err)
			} else {
				resetURL := requestScheme(r) + "://" + r.Host + "/owner/reset-password?token=" + url.QueryEscape(token)
				body := "Someone requested a password reset for your " + plant.Name + " owner login on GatsToGo.\n\n" +
					"Reset your password: " + resetURL + "\n\n" +
					"This link expires in an hour. If you didn't request this, you can safely ignore this email."
				if err := s.Email.Send(r.Context(), ownerEmail, "Reset your GatsToGo password", body); err != nil {
					log.Println("owner forgot password: send email failed:", err)
				}
			}
		}

		http.Redirect(w, r, "/owner/forgot-password?submitted=1", http.StatusSeeOther)
	}
}

func ownerForgotPasswordErrorMessage(code string) string {
	switch code {
	case "missing":
		return "Enter your phone number."
	case "rate-limit":
		return "Too many requests. Please wait a moment and try again."
	case "form":
		return "Something went wrong. Please try again."
	default:
		return ""
	}
}

func ownerResetPasswordPageHandler(s *Server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		plant := middleware.GetPlant(r.Context())
		if plant == nil {
			renderNotFound(w, r)
			return
		}
		csrfToken := middleware.EnsurePublicCSRFCookie(w, r)
		token := r.URL.Query().Get("token")
		// Peek, never Consume, on a GET -- email clients and security
		// scanners routinely pre-fetch links in an email's body; Consume
		// here would let that silently burn the real owner's single-use
		// link before they ever click it themselves (see passwordreset.
		// Peek's own doc comment).
		_, peekErr := passwordreset.Peek(r.Context(), s.Redis, token)
		tokenValid := peekErr == nil
		errorMsg := ownerResetPasswordErrorMessage(r.URL.Query().Get("error"))
		if err := pages.OwnerResetPassword(plant.Name, errorMsg, token, tokenValid, csrfToken).Render(r.Context(), w); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	}
}

func ownerResetPasswordSubmitHandler(s *Server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		plant := middleware.GetPlant(r.Context())
		if plant == nil {
			renderNotFound(w, r)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Redirect(w, r, "/owner/reset-password?error=form", http.StatusSeeOther)
			return
		}
		token := r.FormValue("token")
		newPassword := r.FormValue("new_password")
		confirm := r.FormValue("new_password_confirm")
		back := "/owner/reset-password?token=" + url.QueryEscape(token)

		if newPassword == "" || confirm == "" {
			http.Redirect(w, r, back+"&error=missing", http.StatusSeeOther)
			return
		}
		if newPassword != confirm {
			http.Redirect(w, r, back+"&error=mismatch", http.StatusSeeOther)
			return
		}

		// Consumed here, on the real submit -- this is the only place a
		// reset token is ever actually invalidated (see passwordreset.
		// Consume's own doc comment).
		userID, err := passwordreset.Consume(r.Context(), s.Redis, token)
		if err != nil {
			http.Redirect(w, r, back+"&error=invalid-token", http.StatusSeeOther)
			return
		}

		newHash, err := auth.HashPassword(newPassword)
		if err != nil {
			log.Println("owner reset password: hash failed:", err)
			http.Redirect(w, r, back+"&error=form", http.StatusSeeOther)
			return
		}

		err = tenantdb.WithTenant(r.Context(), s.AppDB, plant.ID, func(ctx context.Context, q tenantdb.Querier) error {
			// The "AND plant_id = $3" clause is the actual defense
			// against a token somehow being replayed against the wrong
			// tenant's subdomain: it makes RowsAffected() come back 0 --
			// treated as an invalid token below -- rather than updating
			// a user this plant doesn't own, without needing a separate
			// lookup to check first.
			tag, err := q.Exec(ctx, `
				UPDATE users SET password_hash = $1
				WHERE id = $2 AND plant_id = $3 AND role IN ('owner', 'manager')
			`, newHash, userID, plant.ID)
			if err != nil {
				return err
			}
			if tag.RowsAffected() == 0 {
				return pgx.ErrNoRows
			}
			return audit.Log(ctx, q, &plant.ID, &userID, "owner.password_reset", "", nil)
		})
		if err != nil {
			log.Println("owner reset password: update failed:", err)
			http.Redirect(w, r, back+"&error=invalid-token", http.StatusSeeOther)
			return
		}

		http.Redirect(w, r, "/owner/login", http.StatusSeeOther)
	}
}

func ownerResetPasswordErrorMessage(code string) string {
	switch code {
	case "missing":
		return "Enter and confirm your new password."
	case "mismatch":
		return "Those passwords don't match."
	case "invalid-token":
		return "That link is invalid or has expired. Request a new one."
	case "form":
		return "Something went wrong. Please try again."
	default:
		return ""
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
