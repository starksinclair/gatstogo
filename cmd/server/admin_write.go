package main

import (
	"errors"
	"log"
	"math"
	"net/http"
	"strconv"
	"strings"

	"gatstogo/internal/middleware"
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
			Name:          r.FormValue("name"),
			Slug:          r.FormValue("slug"),
			City:          r.FormValue("city"),
			Phone:         r.FormValue("phone"),
			Address:       r.FormValue("address"),
			PrimaryColor:  r.FormValue("primary_color"),
			ButtonColor:   r.FormValue("button_color"),
			OwnerName:     r.FormValue("owner_name"),
			OwnerPhone:    r.FormValue("owner_phone"),
			OwnerPassword: r.FormValue("owner_password"),
		}
		if priceErr == nil {
			params.StartingPriceKobo = startingPriceKobo
		}

		_, err := plants.Create(r.Context(), s.AdminDB, params, actor.UserID)
		if err != nil {
			log.Println("admin create plant:", err)
			http.Redirect(w, r, "/admin?error="+adminPlantErrorCode(err)+"#onboarding", http.StatusSeeOther)
			return
		}
		http.Redirect(w, r, "/admin#plants", http.StatusSeeOther)
	}
}

// adminPlantErrorCode maps a plants.Create error to the ?error= code
// adminErrorMessage (main.go) knows how to turn into a specific message,
// rather than collapsing every failure into one generic banner -- these
// are all real, distinct, user-fixable input mistakes.
func adminPlantErrorCode(err error) string {
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
