package main

import (
	"context"
	"errors"
	"log"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"gatstogo/internal/buyers"
	"gatstogo/internal/middleware"
	"gatstogo/internal/plants"
	"gatstogo/web/templates/pages"

	"github.com/redis/go-redis/v9"
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
// apex (APP_BASE_DOMAIN), "www", or local single-label host has no tenant
// to resolve -- middleware.Tenant would 404 there. Those hosts get the
// marketing landing instead.
func rootOrTenant(s *Server, tenantHandler http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if middleware.IsMarketingHost(r.Host, middleware.BaseDomain) {
			marketingLandingHandler(s)(w, r)
			return
		}
		serveTenantOrReview(s, tenantHandler, w, r)
	}
}

// tenantOrReview serves tenant pages that should still explain the
// application's status before a plant is activated. It is intentionally
// only used for human-facing GET pages such as /owner/login and
// /terminal; write/API routes stay inside middleware.Tenant so inactive
// plants cannot mutate anything.
func tenantOrReview(s *Server, tenantHandler http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		serveTenantOrReview(s, tenantHandler, w, r)
	}
}

func serveTenantOrReview(s *Server, tenantHandler http.HandlerFunc, w http.ResponseWriter, r *http.Request) {
	if !middleware.IsTenantHost(r.Host, middleware.BaseDomain) {
		renderNotFound(w, r)
		return
	}

	slug := middleware.ExtractSubdomain(r.Host)
	var name, status string
	err := s.AdminDB.QueryRow(r.Context(), `
		SELECT name, status
		FROM plants
		WHERE slug = $1
	`, slug).Scan(&name, &status)
	if err != nil {
		renderNotFound(w, r)
		return
	}
	if strings.EqualFold(status, "provisioning") {
		if err := pages.PlantUnderReview(name, s.AppBaseURL).Render(r.Context(), w); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
		return
	}
	if !strings.EqualFold(status, "active") {
		renderNotFound(w, r)
		return
	}
	middleware.Tenant(s.AdminDB)(tenantHandler).ServeHTTP(w, r)
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

// marketingHostOnly gates GatsToGo platform routes such as /admin to the
// marketing/base host. A plant subdomain like sportygas.localhost must not
// reach platform-admin auth at all; it should look exactly like an unknown
// tenant route and render the branded 404.
func marketingHostOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !middleware.IsMarketingHost(r.Host, middleware.BaseDomain) {
			renderNotFound(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
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
		if err := pages.About(s.SupportEmail).Render(r.Context(), w); err != nil {
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
			Banks:     s.Paystack.BanksWithFallback(r.Context()),
			Tab:       signupTab(r.URL.Query().Get("for")),
			Source:    r.URL.Query().Get("src"),
		}
		switch r.URL.Query().Get("submitted") {
		case "buyer":
			data.BuyerSubmitted = true
			data.BuyerOfferRank = atoiOr(r.URL.Query().Get("rank"), 0)
			if data.BuyerOfferRank < 0 || data.BuyerOfferRank > buyers.OfferSpots {
				data.BuyerOfferRank = 0
			}
		}
		fillSignupCounters(r.Context(), s, &data)
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
			renderSignupForm(s, w, r, "Could not read that submission. Try again.")
			return
		}

		password := r.FormValue("owner_password")
		if password != r.FormValue("owner_password_confirm") {
			renderSignupForm(s, w, r, "Those passwords don't match.")
			return
		}
		if len(password) < ownerMinPasswordLength {
			renderSignupForm(s, w, r, "Your password needs at least 8 characters.")
			return
		}
		acct := strings.TrimSpace(r.FormValue("bank_account_number"))
		if len(acct) != 10 || strings.Trim(acct, "0123456789") != "" {
			renderSignupForm(s, w, r, "Enter your 10-digit bank account number.")
			return
		}
		if r.FormValue("accept_terms") != "yes" {
			renderSignupForm(s, w, r, "Please agree to the Terms of Service and Privacy Policy to submit your application.")
			return
		}

		startingPriceNaira, priceErr := strconv.ParseFloat(strings.TrimSpace(r.FormValue("starting_price")), 64)
		startingPriceKobo := int64(math.Round(startingPriceNaira * 100))

		params := plants.CreateParams{
			Name:                r.FormValue("name"),
			Slug:                r.FormValue("slug"),
			City:                r.FormValue("city"),
			Phone:               r.FormValue("phone"),
			Address:             r.FormValue("address"),
			State:               r.FormValue("state"),
			LegalBusinessName:   r.FormValue("legal_business_name"),
			CACNumber:           r.FormValue("cac_number"),
			NMDPRALicenseNumber: r.FormValue("nmdpra_license_number"),
			BankCode:            r.FormValue("bank_code"),
			BankAccountNumber:   r.FormValue("bank_account_number"),
			PrimaryColor:        r.FormValue("primary_color"),
			ButtonColor:         r.FormValue("button_color"),
			SecondaryColor:      r.FormValue("secondary_color"),
			ButtonTextColor:     r.FormValue("button_text_color"),
			OwnerName:           r.FormValue("owner_name"),
			OwnerPhone:          r.FormValue("owner_phone"),
			OwnerEmail:          r.FormValue("owner_email"),
			OwnerPassword:       password,
			Status:              "provisioning",
			DeferSettlement:     true,
			ClaimFoundingSpot:   true,
			TermsAcceptedAt:     time.Now(),
		}
		if priceErr == nil {
			params.StartingPriceKobo = startingPriceKobo
		}

		// Bank details are stored as typed; an admin verifies them and
		// creates the Paystack subaccount during review (DeferSettlement).
		// The rate limit still caps junk applications per address.
		if !allowSignupAttempt(r.Context(), s.Redis, clientIP(r)) {
			renderSignupForm(s, w, r, "Too many applications from this connection. Please wait a while and try again, or contact us if you need help.")
			return
		}

		// Logo upload stays admin-only for now because the public form is
		// a normal URL-encoded submission. Brand colors are safe to accept
		// here because plants.Create sanitizes invalid/empty values back to
		// the design-system defaults before anything reaches CSS.
		plant, err := provisionPlant(r.Context(), s, params, nil, nil, nil)
		if err != nil {
			log.Println("signup: create plant failed:", err)
			renderSignupForm(s, w, r, signupErrorMessage(adminPlantErrorCode(err)))
			return
		}

		// Detached from the request: the visitor should not wait on (or be
		// told about a failure of) outgoing email once the application is saved.
		go notifyNewApplication(context.Background(), s, plantApplicationNotification{
			PlantID:           plant.ID,
			PlantName:         params.Name,
			Slug:              plant.Slug,
			City:              params.City,
			State:             params.State,
			OwnerName:         params.OwnerName,
			OwnerPhone:        params.OwnerPhone,
			OwnerEmail:        params.OwnerEmail,
			LegalBusinessName: params.LegalBusinessName,
			CACNumber:         params.CACNumber,
			NMDPRALicense:     params.NMDPRALicenseNumber,
		})

		http.Redirect(w, r, "/get-started?submitted=1", http.StatusSeeOther)
	}
}

// renderSignupForm re-renders the signup page with the visitor's
// non-sensitive input echoed back (never the password fields -- see
// pages.SignupViewData's own doc comment) and an error banner, instead of
// redirecting -- a redirect would need to round-trip every field through
// a query string, which is both awkward and would put a plant's contact
// details in server logs/browser history for no reason.
func renderSignupForm(s *Server, w http.ResponseWriter, r *http.Request, errorMsg string) {
	csrfToken := middleware.EnsurePublicCSRFCookie(w, r)
	data := pages.SignupViewData{
		CSRFToken:           csrfToken,
		ErrorMsg:            errorMsg,
		Name:                r.FormValue("name"),
		Slug:                r.FormValue("slug"),
		City:                r.FormValue("city"),
		Address:             r.FormValue("address"),
		Phone:               r.FormValue("phone"),
		State:               r.FormValue("state"),
		LegalBusinessName:   r.FormValue("legal_business_name"),
		CACNumber:           r.FormValue("cac_number"),
		NMDPRALicenseNumber: r.FormValue("nmdpra_license_number"),
		BankCode:            r.FormValue("bank_code"),
		BankAccountNumber:   r.FormValue("bank_account_number"),
		PrimaryColor:        r.FormValue("primary_color"),
		ButtonColor:         r.FormValue("button_color"),
		SecondaryColor:      r.FormValue("secondary_color"),
		ButtonTextColor:     r.FormValue("button_text_color"),
		OwnerName:           r.FormValue("owner_name"),
		OwnerPhone:          r.FormValue("owner_phone"),
		OwnerEmail:          r.FormValue("owner_email"),
		StartingPrice:       r.FormValue("starting_price"),
		Banks:               s.Paystack.BanksWithFallback(r.Context()),
		Tab:                 "owner",
	}
	fillSignupCounters(r.Context(), s, &data)
	if err := pages.Signup(data).Render(r.Context(), w); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// signupErrorMessage maps the same ?error= codes adminPlantErrorCode
// (cmd/server/admin_write.go) already produces from a provisionPlant/
// plants.Err* failure to copy written for a public visitor instead of an
// internal admin -- "starting price" instead of "starting_price", no
// mention of internal concepts an applicant has no context for.
func signupErrorMessage(code string) string {
	switch code {
	case "missing":
		return "Fill in every field: plant name, web address, business details, bank details, your name, phone number, email, password, and a starting price are all required."
	case "slug":
		return "Your web address can only contain lowercase letters, numbers, and hyphens, and can't start or end with a hyphen."
	case "reserved-slug":
		return "That web address is reserved. Please choose a different one."
	case "slug-taken":
		return "That web address is already taken. Please choose a different one."
	case "price":
		return "Enter a starting price per kg greater than zero."
	case "email":
		return "Enter a valid email address."
	case "bank":
		return "We couldn't verify that bank account. Double-check the bank and account number, and try again."
	case "plant":
		return "Something went wrong submitting your application. Please try again."
	default:
		return ""
	}
}

// signupRateLimitMax/-Window cap how many applications one IP can push
// through to the Paystack-calling part of the signup form. Generous enough
// for a person fixing a mistyped bank account a few times, or an office
// sharing one connection; far too low to farm subaccounts with.
const (
	signupRateLimitMax    = 10
	signupRateLimitWindow = time.Hour
)

// allowSignupAttempt fails open on a Redis error, like the bank-lookup
// endpoint's limiter: a Redis outage shouldn't stop real applications, and
// the error is logged so a persistent problem stays visible.
func allowSignupAttempt(ctx context.Context, rdb *redis.Client, ip string) bool {
	allowed, err := checkIPRateLimit(ctx, rdb, "signup_rl:", ip, signupRateLimitMax, signupRateLimitWindow)
	if err != nil {
		log.Println("signup: rate limit check failed:", err)
		return true
	}
	return allowed
}

// signupTab picks which /get-started tab opens first. Anything but "buyer"
// opens the owner tab, so every existing link keeps working.
func signupTab(v string) string {
	if v == "buyer" {
		return "buyer"
	}
	return "owner"
}

func atoiOr(v string, fallback int) int {
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil {
		return fallback
	}
	return n
}

// fillSignupCounters sets the two independent "spots left" counters. A
// database error leaves the counters at zero claimed rather than failing
// the page, since the counters are decoration, not part of submitting.
func fillSignupCounters(ctx context.Context, s *Server, data *pages.SignupViewData) {
	data.OwnerSpotsTotal = plants.FoundingSpots
	data.BuyerSpotsTotal = buyers.OfferSpots
	if n, err := plants.FoundingSpotsClaimed(ctx, s.AdminDB); err == nil {
		data.OwnerSpotsClaimed = n
	} else {
		log.Println("signup: founding counter:", err)
	}
	if n, err := buyers.SpotsClaimed(ctx, s.AdminDB); err == nil {
		data.BuyerSpotsClaimed = n
	} else {
		log.Println("signup: buyer counter:", err)
	}
}

// ---- POST /get-started/buyer (public: a gas buyer registers) ----

func buyerSignupHandler(s *Server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		fail := func(msg string) {
			csrfToken := middleware.EnsurePublicCSRFCookie(w, r)
			data := pages.SignupViewData{
				CSRFToken:     csrfToken,
				Tab:           "buyer",
				BuyerErrorMsg: msg,
				BuyerName:     r.FormValue("buyer_name"),
				BuyerPhone:    r.FormValue("buyer_phone"),
				BuyerEmail:    r.FormValue("buyer_email"),
				BuyerState:    r.FormValue("buyer_state"),
				Source:        r.FormValue("src"),
				Banks:         s.Paystack.BanksWithFallback(r.Context()),
			}
			fillSignupCounters(r.Context(), s, &data)
			if err := pages.Signup(data).Render(r.Context(), w); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
			}
		}
		if err := r.ParseForm(); err != nil {
			fail("Could not read that submission. Try again.")
			return
		}
		if r.FormValue("accept_contact") != "yes" {
			fail("Please tick the box so we can text you when a plant near you joins.")
			return
		}
		if !allowSignupAttempt(r.Context(), s.Redis, clientIP(r)) {
			fail("Too many sign-ups from this connection. Please wait a while and try again.")
			return
		}
		res, err := buyers.Signup(r.Context(), s.AdminDB, buyers.SignupParams{
			Name:   r.FormValue("buyer_name"),
			Phone:  r.FormValue("buyer_phone"),
			Email:  r.FormValue("buyer_email"),
			State:  r.FormValue("buyer_state"),
			Source: r.FormValue("src"),
		})
		if err != nil {
			switch {
			case errors.Is(err, buyers.ErrMissingField):
				fail("Enter your name.")
			case errors.Is(err, buyers.ErrInvalidPhone):
				fail("Enter a valid Nigerian phone number, for example 0803 000 0000.")
			case errors.Is(err, buyers.ErrInvalidEmail):
				fail("That email address doesn't look right. You can also leave it blank.")
			case errors.Is(err, buyers.ErrInvalidState):
				fail("Choose your state.")
			default:
				log.Println("buyer signup:", err)
				fail("Something went wrong. Please try again.")
			}
			return
		}
		http.Redirect(w, r, "/get-started?submitted=buyer&for=buyer&rank="+strconv.Itoa(res.OfferRank), http.StatusSeeOther)
	}
}
