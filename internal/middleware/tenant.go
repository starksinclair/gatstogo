package middleware

import (
	"context"
	"log"
	"net/http"
	"strings"

	"gatstogo/internal/domain"
	"gatstogo/web/templates/pages"

	"github.com/jackc/pgx/v5/pgxpool"
)

type contextKey string

const PlantContextKey contextKey = "plant"

// BaseDomain is this app's own real domain -- "gatstogo.ng" in
// production, "localhost" for the local dev loop (sunrise.localhost:8080)
// -- set once at startup from APP_BASE_DOMAIN (cmd/server/main.go)
// before Tenant is ever invoked. Every legitimate tenant request's Host
// header has the shape "<slug>.<BaseDomain>[:port]"; Tenant below refuses
// to resolve a plant for anything else.
//
// This check is the fix for a real, confirmed Host-header-injection
// vulnerability: before it existed, Tenant resolved a plant using only
// the leftmost label of r.Host (ExtractSubdomain), with nothing checking
// that the rest of the host was actually this app's own domain. A request
// with Host: sunrise.evil.com resolved the real "sunrise" plant just as
// successfully as sunrise.gatstogo.ng would have -- and since several
// handlers build absolute URLs from r.Host (most seriously, the owner
// "forgot password" email's reset link, cmd/server/auth.go), an attacker
// could get a real, valid password-reset link emailed to a real owner
// with attacker-controlled host, a textbook password-reset-poisoning
// setup: whoever controls that host captures the token the moment the
// owner clicks it. Confirmed exploitable end-to-end (including a real
// email delivery) against this app before this fix.
var BaseDomain string

func Tenant(db *pgxpool.Pool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			host := r.Host // e.g. sunrise.gatstogo.ng or sunrise.localhost:8080

			slug := ExtractSubdomain(host)
			if slug == "" || slug == "www" || slug == "admin" || slug == "api" || !hasBaseDomainSuffix(host, BaseDomain) {
				// "" and "www" are the marketing host -- cmd/server's
				// rootOrTenant handler intercepts / and /home for those
				// before this middleware ever runs, so reaching here with
				// no slug means some other tenant-only route (e.g.
				// /terminal, /owner/login) was hit directly on the bare
				// domain. Same branded 404 as an unresolved subdomain, and
				// -- deliberately -- the same 404 a spoofed-host request
				// now gets too (see BaseDomain's own doc comment): there's
				// no legitimate case where telling an attacker "that host
				// doesn't match, but the slug would otherwise be valid" is
				// the right amount of information to hand back.
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

// hasBaseDomainSuffix reports whether host is a direct subdomain of
// baseDomain -- "sunrise.gatstogo.ng" of "gatstogo.ng", but not
// "sunrise.evilgatstogo.ng" (no dot boundary before the match) or
// "sunrise.evil.com" (no match at all) of the same baseDomain. Fails
// closed (false) if baseDomain is empty -- a misconfigured/unset
// BaseDomain must never make every host pass this check by accident.
func hasBaseDomainSuffix(host, baseDomain string) bool {
	if baseDomain == "" {
		return false
	}
	if colonIndex := strings.Index(host, ":"); colonIndex != -1 {
		host = host[:colonIndex]
	}
	return strings.HasSuffix(host, "."+baseDomain)
}
