package main

import (
	"context"
	"errors"
	"log"
	"math"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"

	"gatstogo/internal/middleware"
	"gatstogo/internal/payments"
	"gatstogo/internal/plants"
	"gatstogo/internal/uploads"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// Like the owner write handlers (cmd/server/owner_write.go), these are
// real <form method="post"> submissions, not JSON -- they redirect back
// to /admin#<tab> (?error=<code> on failure, mapped by adminErrorMessage
// in main.go).
//
// Unlike every owner-side write, these run directly against s.AdminDB,
// not through tenantdb.WithTenant -- see plants.Create's own doc comment
// for why plant creation specifically can't be tenant-scoped (there's no
// plant id yet to scope to), and plants.SetStatus is a platform-level
// operation keyed by plant id with no per-request tenant to enforce
// either.

// ---- POST /admin/plants ----

func adminCreatePlantHandler(s *Server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		actor := middleware.GetActor(r.Context())
		if actor == nil {
			http.Error(w, "Not found", http.StatusNotFound)
			return
		}
		// ParseMultipartForm, not ParseForm: the form now carries an
		// optional logo file (enctype="multipart/form-data",
		// owner_admin.templ). r.FormValue(...) below works exactly the
		// same either way -- note this call is largely redundant in
		// practice, since middleware.CSRF's own r.FormValue("csrf_token")
		// already triggered the real parse (with Go's default 32MiB
		// threshold) before this handler ever runs; kept for the explicit
		// error handling and as a clear signal, to a reader, that this
		// handler expects a multipart body.
		if err := r.ParseMultipartForm(uploads.MaxLogoBytes); err != nil {
			http.Redirect(w, r, "/admin?error=form#onboarding", http.StatusSeeOther)
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
			OwnerPassword:       r.FormValue("owner_password"),
		}
		if priceErr == nil {
			params.StartingPriceKobo = startingPriceKobo
		}

		logoFile, logoHeader := extractOptionalLogo(r)
		if logoFile != nil {
			defer logoFile.Close()
		}

		_, err := provisionPlant(r.Context(), s, params, logoFile, logoHeader, &actor.UserID)
		if err != nil {
			log.Println("admin create plant:", err)
			http.Redirect(w, r, "/admin?error="+adminPlantErrorCode(err)+"#onboarding", http.StatusSeeOther)
			return
		}
		http.Redirect(w, r, "/admin#plants", http.StatusSeeOther)
	}
}

// extractOptionalLogo reads the "logo" file field, if the visitor
// actually attached one -- an empty file input is not an error
// (http.ErrMissingFile), just "no logo this time", the same optional
// treatment as an unfilled City/Address field.
func extractOptionalLogo(r *http.Request) (multipart.File, *multipart.FileHeader) {
	file, header, err := r.FormFile("logo")
	if err != nil {
		return nil, nil
	}
	return file, header
}

// bankVerificationError wraps a Paystack failure from provisionPlant's own
// ResolveAccount/CreateSubaccount calls, distinct from a plants.Err*, so
// adminPlantErrorCode (and signupErrorMessage, which reuses it) can show
// "we couldn't verify that bank account" instead of a generic
// plant-creation failure.
type bankVerificationError struct{ err error }

func (e *bankVerificationError) Error() string {
	return "provision plant: bank verification failed: " + e.err.Error()
}
func (e *bankVerificationError) Unwrap() error { return e.err }

// logoUploadError wraps a rejected logo upload (internal/uploads.SaveLogo)
// -- distinct from a plants.Err*/bankVerificationError so
// adminPlantErrorCode can show a specific "that image didn't work"
// message instead of a generic plant-creation failure. Unlike a bad bank
// account, a bad logo never had to reach here: the visitor could always
// have just left the field empty (LogoPath is optional, plants.Create's
// own validation never requires it) -- provisionPlant still treats a
// *rejected* upload as a hard stop rather than silently dropping it,
// though, so a plant is never created with a visitor believing their logo
// was saved when it actually wasn't.
type logoUploadError struct{ err error }

func (e *logoUploadError) Error() string {
	return "provision plant: logo upload failed: " + e.err.Error()
}
func (e *logoUploadError) Unwrap() error { return e.err }

// provisionPlant orchestrates plant creation: resolves the submitted bank
// account and creates its Paystack subaccount before writing anything to
// the database, then delegates to plants.Create for the actual atomic
// write. Shared by both onboarding entry points -- the admin console's
// "Create plant" panel (adminCreatePlantHandler above) and the public
// self-serve /get-started form (cmd/server/marketing.go's
// signupSubmitHandler) -- since both collect the exact same
// business/bank fields and need the exact same sequencing. internal/plants
// itself stays free of a Paystack dependency (matches its existing scope),
// so this orchestration lives here instead, the same pattern
// cmd/server/tickets.go's buyGasSubmitHandler already uses for "create the
// DB row, then call Paystack, accept the two aren't atomically linked" --
// here inverted (call Paystack, then write the DB row), because unlike a
// ticket a plant must never exist without a real settlement path already
// attached (see plants.Create's own second validation block).
//
// plants.ValidateParams runs first, before any Paystack call or logo save:
// calling CreateSubaccount for a submission that's missing a required
// field would create a real, orphaned Paystack subaccount for nothing,
// and saving a logo to disk under a slug that's about to be rejected
// (reserved/taken -- the one thing ValidateParams itself can't catch,
// since that requires a database check) would leave a stray file behind
// for the same reason. If the database write somehow fails *after* a
// successful CreateSubaccount or logo save, both are simply left orphaned
// -- harmless (costs nothing, references nothing), the same accepted
// trade-off buyGasSubmitHandler's own ticket-then-Paystack sequencing
// already lives with in the other direction.
//
// logoFile/logoHeader are both nilable -- a plant with no logo attached
// is the normal case (LogoPath is optional; see CreateParams's own doc
// comment), not an error.
func provisionPlant(ctx context.Context, s *Server, params plants.CreateParams, logoFile multipart.File, logoHeader *multipart.FileHeader, actorID *uuid.UUID) (*plants.Plant, error) {
	if err := plants.ValidateParams(params); err != nil {
		return nil, err
	}

	if logoFile != nil {
		// Normalized (trimmed, lowercased) the same way plants.Create
		// itself normalizes a slug internally -- using the raw,
		// as-submitted params.Slug here instead would save the file
		// under a path that doesn't match the slug plants.Create
		// actually stores (e.g. mixed-case "Sunrise" on disk vs. the
		// lowercased "sunrise" every tenant lookup, and every other
		// plant, actually uses), and plants.logo_path would then point
		// at a URL that 404s.
		slug := strings.ToLower(strings.TrimSpace(params.Slug))
		logoPath, err := uploads.SaveLogo(slug, logoFile, logoHeader)
		if err != nil {
			return nil, &logoUploadError{err}
		}
		params.LogoPath = logoPath
	}

	bankCode := strings.TrimSpace(params.BankCode)
	accountNumber := strings.TrimSpace(params.BankAccountNumber)

	resolved, err := s.Paystack.ResolveAccount(ctx, accountNumber, bankCode)
	if err != nil {
		return nil, &bankVerificationError{err}
	}

	sub, err := s.Paystack.CreateSubaccount(ctx, payments.CreateSubaccountParams{
		BusinessName:     strings.TrimSpace(params.LegalBusinessName),
		SettlementBank:   bankCode,
		AccountNumber:    accountNumber,
		PercentageCharge: payments.PlantSettlementPercentage,
	})
	if err != nil {
		return nil, &bankVerificationError{err}
	}

	params.BankName = s.Paystack.BankName(ctx, bankCode)
	params.BankAccountName = resolved.AccountName
	params.PaystackSubaccountCode = sub.SubaccountCode

	return plants.Create(ctx, s.AdminDB, params, actorID)
}

// adminPlantErrorCode maps a provisionPlant/plants.Create error to the
// ?error= code adminErrorMessage (main.go) knows how to turn into a
// specific message, rather than collapsing every failure into one generic
// banner -- these are all real, distinct, user-fixable input mistakes.
// Also used by cmd/server/marketing.go's signupErrorMessage, since both
// onboarding entry points share the exact same error surface.
func adminPlantErrorCode(err error) string {
	var bankErr *bankVerificationError
	if errors.As(err, &bankErr) {
		return "bank"
	}
	var logoErr *logoUploadError
	if errors.As(err, &logoErr) {
		return "logo"
	}
	switch {
	case errors.Is(err, plants.ErrMissingField):
		return "missing"
	case errors.Is(err, plants.ErrInvalidSlug):
		return "slug"
	case errors.Is(err, plants.ErrReservedSlug):
		return "reserved-slug"
	case errors.Is(err, plants.ErrSlugTaken):
		return "slug-taken"
	case errors.Is(err, plants.ErrInvalidPrice):
		return "price"
	case errors.Is(err, plants.ErrInvalidEmail):
		return "email"
	default:
		return "plant"
	}
}

// ---- POST /admin/plants/{id}/status ----

func adminPlantStatusHandler(s *Server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		actor := middleware.GetActor(r.Context())
		if actor == nil {
			http.Error(w, "Not found", http.StatusNotFound)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Redirect(w, r, "/admin?error=form#plants", http.StatusSeeOther)
			return
		}
		plantID, err := uuid.Parse(chi.URLParam(r, "id"))
		if err != nil {
			http.Redirect(w, r, "/admin?error=status#plants", http.StatusSeeOther)
			return
		}
		status := r.FormValue("status")

		if err := plants.SetStatus(r.Context(), s.AdminDB, plantID, status, actor.UserID); err != nil {
			log.Println("admin plant status:", err)
			http.Redirect(w, r, "/admin?error=status#plants", http.StatusSeeOther)
			return
		}
		http.Redirect(w, r, "/admin#plants", http.StatusSeeOther)
	}
}
