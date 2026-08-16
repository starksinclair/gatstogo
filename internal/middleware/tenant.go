package middleware

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strings"

	"gatstogo/internal/domain"
	"gatstogo/web/templates/pages"

	"github.com/jackc/pgx/v5/pgxpool"
)

type contextKey string

const PlantContextKey contextKey = "plant"

func Tenant(db *pgxpool.Pool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			host := r.Host // e.g. sunrise.gatstogo.ng or sunrise.localhost:8080
			fmt.Print("Host: ", host, "\n")

			slug := ExtractSubdomain(host)
			if slug == "" || slug == "www" || slug == "admin" || slug == "api" {
				// "" and "www" are the marketing host -- cmd/server's
				// rootOrTenant handler intercepts / and /home for those
				// before this middleware ever runs, so reaching here with
				// no slug means some other tenant-only route (e.g.
				// /terminal, /owner/login) was hit directly on the bare
				// domain. Same branded 404 as an unresolved subdomain.
				renderNotFound(w, r)
				return
			}

			var plant domain.Plant
			err := db.QueryRow(r.Context(), `
				SELECT id, slug, name, city, address, phone, logo_path,
				       custom_domain, domain_status, status, timezone,
				       primary_color, secondary_color, button_color, button_text_color, font_family,
				       paystack_subaccount_code,
				       created_at, updated_at
				FROM plants
				WHERE slug = $1 AND status = 'active'
			`, slug).Scan(
				&plant.ID, &plant.Slug, &plant.Name, &plant.City, &plant.Address,
				&plant.Phone, &plant.LogoPath, &plant.CustomDomain, &plant.DomainStatus,
				&plant.Status, &plant.Timezone, &plant.PrimaryColor, &plant.SecondaryColor,
				&plant.ButtonColor, &plant.ButtonTextColor, &plant.FontFamily,
				&plant.PaystackSubaccountCode, &plant.CreatedAt, &plant.UpdatedAt,
			)

			if err != nil {
				renderNotFound(w, r)
				return
			}

			// Put plant into context
			ctx := context.WithValue(r.Context(), PlantContextKey, &plant)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// GetPlant Helper to get plant from context
func GetPlant(ctx context.Context) *domain.Plant {
	plant, ok := ctx.Value(PlantContextKey).(*domain.Plant)
	if !ok {
		return nil
	}
	return plant
}

// renderNotFound writes the one branded 404 page (web/templates/pages/
// not_found.templ) -- the single replacement for what used to be a bare
// http.Error(w, "Plant not found", ...) scattered across this middleware
// and several cmd/server handlers. Status is set before Render is called
// since Render only ever fails on a write error at that point (the
// header can't be changed after bytes have already gone out).
func renderNotFound(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusNotFound)
	if err := pages.NotFound().Render(r.Context(), w); err != nil {
		log.Println("render not-found page:", err)
	}
}

// ExtractSubdomain pulls the leftmost label off a request Host header
// (e.g. "sunrise" from "sunrise.gatstogo.ng" or "sunrise.localhost:8080").
// Exported so cmd/server's own routing can ask the same question Tenant
// asks internally -- "is this a real tenant subdomain, or the bare/www
// marketing host?" -- without duplicating the parsing logic.
func ExtractSubdomain(host string) string {
	// Remove port if present
	if colonIndex := strings.Index(host, ":"); colonIndex != -1 {
		host = host[:colonIndex]
	}

	parts := strings.Split(host, ".")
	if len(parts) < 2 {
		return ""
	}
	return parts[0]
}
