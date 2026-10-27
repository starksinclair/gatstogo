package pages

import "gatstogo/internal/assets"

// cssURL is a versioned stylesheet URL (see internal/assets).
func cssURL(name string) string {
	return assets.URL("/static/css/" + name)
}
