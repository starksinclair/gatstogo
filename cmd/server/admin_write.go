package main

import (
	"context"
	"errors"
	"log"
	"math"
	"net/http"
	"strconv"
	"strings"

	"gatstogo/internal/middleware"
	"gatstogo/internal/payments"
	"gatstogo/internal/plants"

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
		if err := r.ParseForm(); err != nil {
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
			OwnerName:           r.FormValue("owner_name"),
			OwnerPhone:          r.FormValue("owner_phone"),
			OwnerEmail:          r.FormValue("owner_email"),
			OwnerPassword:       r.FormValue("owner_password"),
		}
		if priceErr == nil {
			params.StartingPriceKobo = startingPriceKobo
		}

		_, err := provisionPlant(r.Context(), s, params, &actor.UserID)
		if err != nil {
			log.Println("admin create plant:", err)
			http.Redirect(w, r, "/admin?error="+adminPlantErrorCode(err)+"#onboarding", http.StatusSeeOther)
			return
		}
		http.Redirect(w, r, "/admin#plants", http.StatusSeeOther)
	}
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
// plants.ValidateParams runs first, before any Paystack call: calling
// CreateSubaccount for a submission that's missing a required field would
// create a real, orphaned Paystack subaccount for nothing. If the
// database write somehow fails *after* a successful CreateSubaccount, the
// subaccount is simply left orphaned at Paystack -- harmless (costs
// nothing, references nothing), the same accepted trade-off
// buyGasSubmitHandler's own ticket-then-Paystack sequencing already lives
// with in the other direction.
func provisionPlant(ctx context.Context, s *Server, params plants.CreateParams, actorID *uuid.UUID) (*plants.Plant, error) {
	if err := plants.ValidateParams(params); err != nil {
		return nil, err
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
