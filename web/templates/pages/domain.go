package pages

import "strings"

// AppBaseDomain is the apex host for tenant URLs (e.g. "gatstogo.com" in
// production, "localhost" for local dev). Set once at startup from
// APP_BASE_DOMAIN in cmd/server/main.go — the same value assigned to
// middleware.BaseDomain. Templates and help copy read this so the public
// domain can be swapped without hunting string literals.
//
// Default is the production apex so unit tests and static previews still
// render sensible examples before main runs.
var AppBaseDomain = "gatstogo.com"

const baseDomainPlaceholder = "{{base_domain}}"

// helpText substitutes the configured apex into help-centre copy that uses
// {{base_domain}} placeholders (e.g. "sunrise.{{base_domain}}").
func helpText(s string) string {
	domain := strings.TrimSpace(AppBaseDomain)
	if domain == "" {
		domain = "gatstogo.com"
	}
	return strings.ReplaceAll(s, baseDomainPlaceholder, domain)
}
