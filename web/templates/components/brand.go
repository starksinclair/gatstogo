package components

// Brand asset paths under web/static/images/gatstogo-logo.
// Prefer SVG marks in UI; PNG sizes are for favicons / PWA / places that
// cannot take SVG. See that folder's README for colour and clear-space rules.

const brandDir = "/static/images/gatstogo-logo"

// Brand mark variants (square icon).
const (
	BrandMarkPrimary       = brandDir + "/svg/gatstogo-mark-primary.svg"        // light UI, default
	BrandMarkStandalone    = brandDir + "/svg/gatstogo-mark-standalone.svg"     // light UI, no container
	BrandMarkReversed      = brandDir + "/svg/gatstogo-mark-reversed.svg"       // dark / photo backgrounds
	BrandMarkDarkContainer = brandDir + "/svg/gatstogo-mark-dark-container.svg" // dark charcoal UI (rail, splash)
	BrandMarkAlert         = brandDir + "/svg/gatstogo-mark-alert.svg"          // reconciliation / alerts only
	BrandMarkMonoBlack     = brandDir + "/svg/gatstogo-mark-mono-black.svg"
	BrandMarkMonoWhite     = brandDir + "/svg/gatstogo-mark-mono-white.svg"
)

// Lockups (mark + wordmark). Min ~96px wide; below that use a mark alone.
const (
	BrandLockupLight = brandDir + "/svg/gatstogo-lockup-light.svg"
	BrandLockupDark  = brandDir + "/svg/gatstogo-lockup-dark.svg"
)

// Raster sizes for favicon / apple-touch / PWA.
const (
	BrandFaviconICO = brandDir + "/favicon.ico"
	BrandMarkPNG16  = brandDir + "/png/gatstogo-mark-16.png"
	BrandMarkPNG32  = brandDir + "/png/gatstogo-mark-32.png"
	BrandMarkPNG48  = brandDir + "/png/gatstogo-mark-48.png"
	BrandMarkPNG64  = brandDir + "/png/gatstogo-mark-64.png"
	BrandMarkPNG128 = brandDir + "/png/gatstogo-mark-128.png"
	BrandMarkPNG192 = brandDir + "/png/gatstogo-mark-192.png"
	BrandMarkPNG256 = brandDir + "/png/gatstogo-mark-256.png"
	BrandMarkPNG512 = brandDir + "/png/gatstogo-mark-512.png"
)
