package middleware

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"gatstogo/internal/domain"

	"github.com/jackc/pgx/v5/pgxpool"
)

type contextKey string

const PlantContextKey contextKey = "plant"

func Tenant(db *pgxpool.Pool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			host := r.Host // e.g. sunrise.gatstogo.ng or sunrise.localhost:8080
			fmt.Print("Host: ", host, "\n")

			slug := extractSubdomain(host)
			if slug == "" || slug == "www" || slug == "admin" || slug == "api" {
				http.Error(w, "Plant not found", http.StatusNotFound)
				return
			}

			var plant domain.Plant
			err := db.QueryRow(r.Context(), `
				SELECT id, slug, name, city, address, phone, logo_path,
				       custom_domain, domain_status, status, timezone,
				       primary_color, secondary_color, button_color, button_text_color, font_family,
				       created_at, updated_at
				FROM plants
				WHERE slug = $1 AND status = 'active'
			`, slug).Scan(
				&plant.ID, &plant.Slug, &plant.Name, &plant.City, &plant.Address,
				&plant.Phone, &plant.LogoPath, &plant.CustomDomain, &plant.DomainStatus,
				&plant.Status, &plant.Timezone, &plant.PrimaryColor, &plant.SecondaryColor,
				&plant.ButtonColor, &plant.ButtonTextColor, &plant.FontFamily, &plant.CreatedAt, &plant.UpdatedAt,
			)

			if err != nil {
				http.Error(w, "Plant not found", http.StatusNotFound)
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

func extractSubdomain(host string) string {
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
