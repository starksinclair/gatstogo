package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"

	"gatstogo/internal/audit"
	"gatstogo/internal/middleware"
	"gatstogo/internal/reviews"
	"gatstogo/internal/tenantdb"
	"gatstogo/web/templates/pages"

	"github.com/go-chi/chi/v5"
)

func reviewPageHandler(s *Server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		plant := middleware.GetPlant(r.Context())
		if plant == nil {
			renderNotFound(w, r)
			return
		}
		token := strings.TrimSpace(chi.URLParam(r, "token"))
		data := pages.PlantReviewData{
			Plant:     plant,
			CSRFToken: middleware.EnsurePublicCSRFCookie(w, r),
			Token:     token,
		}

		err := tenantdb.WithTenant(r.Context(), s.AppDB, plant.ID, func(ctx context.Context, q tenantdb.Querier) error {
			target, err := reviews.GetTarget(ctx, q, plant.ID, token)
			if err != nil {
				return err
			}
			data.TicketReference = target.TicketReference
			data.AlreadySubmitted = target.SubmittedAt != nil
			if target.Rating != nil {
				data.Rating = *target.Rating
			}
			data.Note = target.Note
			return nil
		})
		if errors.Is(err, reviews.ErrNotFound) {
			renderNotFound(w, r)
			return
		}
		if err != nil {
			log.Println("review page:", err)
			http.Error(w, "Could not load review", http.StatusInternalServerError)
			return
		}
		data.ErrorMessage = reviewErrorMessage(r.URL.Query().Get("error"))
		if err := pages.PlantReview(data).Render(r.Context(), w); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	}
}

func reviewSubmitHandler(s *Server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		plant := middleware.GetPlant(r.Context())
		if plant == nil {
			renderNotFound(w, r)
			return
		}
		token := strings.TrimSpace(chi.URLParam(r, "token"))
		if err := r.ParseForm(); err != nil {
			http.Redirect(w, r, "/reviews/"+token+"?error=form", http.StatusSeeOther)
			return
		}
		rating, err := strconv.Atoi(strings.TrimSpace(r.FormValue("rating")))
		if err != nil {
			http.Redirect(w, r, "/reviews/"+token+"?error=rating", http.StatusSeeOther)
			return
		}
		note := r.FormValue("note")

		err = tenantdb.WithTenant(r.Context(), s.AppDB, plant.ID, func(ctx context.Context, q tenantdb.Querier) error {
			if err := reviews.Submit(ctx, q, plant.ID, token, rating, note); err != nil {
				return err
			}
			return audit.Log(ctx, q, &plant.ID, nil, "review.submitted", token, map[string]any{"rating": rating})
		})
		switch {
		case err == nil:
			http.Redirect(w, r, "/reviews/"+token+"?submitted=1", http.StatusSeeOther)
		case errors.Is(err, reviews.ErrAlreadySubmitted):
			http.Redirect(w, r, "/reviews/"+token, http.StatusSeeOther)
		case errors.Is(err, reviews.ErrInvalidRating):
			http.Redirect(w, r, "/reviews/"+token+"?error=rating", http.StatusSeeOther)
		case errors.Is(err, reviews.ErrNotFound):
			renderNotFound(w, r)
		default:
			log.Println("review submit:", err)
			http.Redirect(w, r, "/reviews/"+token+"?error=server", http.StatusSeeOther)
		}
	}
}

func reviewErrorMessage(code string) string {
	switch code {
	case "rating":
		return "Choose a rating from 1 to 5 stars."
	case "form", "server":
		return "We could not save your review just now. Please try again."
	default:
		return ""
	}
}
