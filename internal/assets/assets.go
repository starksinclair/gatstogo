// Package assets builds cache-busting URLs for static files.
package assets

import (
	"strconv"
	"time"
)

// Version changes on every server start (so on every deploy, and on every
// hot reload in dev). Browsers see a new URL and can't keep using a stale
// cached stylesheet after the CSS changes.
var Version = strconv.FormatInt(time.Now().Unix(), 36)

// URL returns path with the current version appended, e.g.
// "/static/css/tokens.css?v=sk3f9a".
func URL(path string) string {
	return path + "?v=" + Version
}
