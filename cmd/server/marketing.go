package main

import (
	"log"
	"math"
	"net/http"
	"strconv"
	"strings"

	"gatstogo/internal/middleware"
	"gatstogo/internal/plants"
	"gatstogo/web/templates/pages"
)

// This file holds GatsToGo's own public marketing site (landing, pricing,
// about, legal, self-serve signup) plus the routing glue that lets "/"
// and "/home" serve either that or the existing tenant customer-home
// page, depending on which host the request came in on. Every other
// tenant route is untouched -- they only ever make sense on a real
// tenant subdomain, and middleware.Tenant's existing 404 behavior for
// them is correct as-is.

// rootOrTenant is what "/" and "/home" are actually registered against
// (see MountHandlers in main.go) instead of sitting inside the
// tenant-scoped route group like every other customer route does. A bare
// or "www" host has no tenant to resolve at all -- there's nothing for
// middleware.Tenant to do there but 404, which is exactly the gap this
// closes: those hosts now get the real marketing site instead.
func rootOrTenant(s *Server, tenantHandler http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		slug := middleware.ExtractSubdomain(r.Host)
		if slug == "" || slug == "www" {
			marketingLandingHandler(s)(w, r)
			return
		}
		middleware.Tenant(s.AdminDB)(tenantHandler).ServeHTTP(w, r)
	}
}

// renderNotFound is cmd/server's own copy of the same one-line render
// internal/middleware/tenant.go uses -- kept as a small local duplicate
// rather than exporting the middleware package's version, the same
// precedent internal/plants.go's validHexColor doc comment already
// explains for this codebase's other small cross-package helpers.
func renderNotFound(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusNotFound)
	if err := pages.NotFound().Render(r.Context(), w); err != nil {
		log.Println("render not-found page:", err)
	}
}

// ---- GET / (and /home) on the marketing host ----

func marketingLandingHandler(s *Server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := pages.Landing().Render(r.Context(), w); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	}
}

// ---- GET /pricing, /about, /terms, /privacy ----

func pricingPageHandler(s *Server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := pages.Pricing().Render(r.Context(), w); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	}
}

func aboutPageHandler(s *Server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := pages.About(s.NotificationEmail).Render(r.Context(), w); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	}
}

func termsPageHandler(s *Server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := pages.TermsOfService().Render(r.Context(), w); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	}
}

func privacyPageHandler(s *Server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := pages.PrivacyPolicy().Render(r.Context(), w); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	}
}

// ---- GET /get-started (public: the signup form, or its submitted state) ----

func signupPageHandler(s *Server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		csrfToken := middleware.EnsurePublicCSRFCookie(w, r)
		data := pages.SignupViewData{
			CSRFToken: csrfToken,
			ErrorMsg:  signupErrorMessage(r.URL.Query().Get("error")),
			Submitted: r.URL.Query().Get("submitted") == "1",
		}
		if err := pages.Signup(data).Render(r.Context(), w); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	}
}

// ---- POST /get-started (public: submit the application) ----

// signupSubmitHandler reuses internal/plants.Create -- the exact same
// validated, atomic "plant + owner user + starting price" write the
// admin console's onboarding panel already does (cmd/server/admin_write.go)
// -- with two differences: Status is "provisioning" instead of the
// default "active" (so the new plant's subdomain won't resolve until an
// admin reviews and approves it, the same gate plants.CreateParams.Status's
// own doc comment describes), and actorID is nil (no authenticated actor
// exists yet for a public submission).
//
// Deliberately no new rate-limiter here: the realistic abuse case is an
// inert "provisioning" row an admin declines, not fraud or real payment
// risk (a provisioning plant's subdomain never resolves, so it can never
// actually take money) -- and the existing unique-slug + reserved-slug
// checks inside plants.Create already block the obvious spam case of
// re-using a real or reserved name.
func signupSubmitHandler(s *Server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			renderSignupForm(w, r, "Could not read that submission. Try again.")
			return
		}

		password := r.FormValue("owner_password")
		if password != r.FormValue("owner_password_confirm") {
			renderSignupForm(w, r, "Those passwords don't match.")
			return
		}

		startingPriceNaira, priceErr := strconv.ParseFloat(strings.TrimSpace(r.FormValue("starting_price")), 64)
		startingPriceKobo := int64(math.Round(startingPriceNaira * 100))

		params := plants.CreateParams{
			Name:          r.FormValue("name"),
			Slug:          r.FormValue("slug"),
			City:          r.FormValue("city"),
			Phone:         r.FormValue("phone"),
			Address:       r.FormValue("address"),
			OwnerName:     r.FormValue("owner_name"),
			OwnerPhone:    r.FormValue("owner_phone"),
			OwnerPassword: password,
			Status:        "provisioning",
		}
		if priceErr == nil {
			params.StartingPriceKobo = startingPriceKobo
		}

		_, err := plants.Create(r.Context(), s.AdminDB, params, nil)
		if err != nil {
			log.Println("signup: create plant failed:", err)
			renderSignupForm(w, r, signupErrorMessage(adminPlantErrorCode(err)))
			return
		}

		http.Redirect(w, r, "/get-started?submitted=1", http.StatusSeeOther)
	}
}

// renderSignupForm re-renders the signup page with the visitor's
// non-sensitive input echoed back (never the password fields -- see
// pages.SignupViewData's own doc comment) and an error banner, instead of
// redirecting -- a redirect would need to round-trip every field through
// a query string, which is both awkward and would put a plant's contact
// details in server logs/browser history for no reason.
func renderSignupForm(w http.ResponseWriter, r *http.Request, errorMsg string) {
	csrfToken := middleware.EnsurePublicCSRFCookie(w, r)
	data := pages.SignupViewData{
		CSRFToken:     csrfToken,
		ErrorMsg:      errorMsg,
		Name:          r.FormValue("name"),
		Slug:          r.FormValue("slug"),
		City:          r.FormValue("city"),
		Address:       r.FormValue("address"),
		Phone:         r.FormValue("phone"),
		OwnerName:     r.FormValue("owner_name"),
		OwnerPhone:    r.FormValue("owner_phone"),
		StartingPrice: r.FormValue("starting_price"),
	}
	if err := pages.Signup(data).Render(r.Context(), w); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// signupErrorMessage maps the same ?error= codes adminPlantErrorCode
// (cmd/server/admin_write.go) already produces from a plants.Err* to
// copy written for a public visitor instead of an internal admin --
// "starting price" instead of "starting_price", no mention of internal
// concepts an applicant has no context for.
func signupErrorMessage(code string) string {
	switch code {
	case "missing":
		return "Fill in every field -- plant name, web address, your name, phone number, password, and a starting price are all required."
	case "slug":
		return "Your web address can only contain lowercase letters, numbers, and hyphens, and can't start or end with a hyphen."
	case "reserved-slug":
		return "That web address is reserved. Please choose a different one."
	case "slug-taken":
		return "That web address is already taken. Please choose a different one."
	case "price":
		return "Enter a starting price per kg greater than zero."
	case "plant":
		return "Something went wrong submitting your application. Please try again."
	default:
		return ""
	}
}
