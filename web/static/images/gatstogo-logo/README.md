# GatsToGo — logo assets

## What it means

A gas cylinder carrying two bars of equal length. The cylinder gives instant
category recognition; the two equal bars are the product's core idea — gas that
left the tank, and money that came back, agreeing.

---

## Files

### `svg/` — use these wherever possible

| File | Use |
|---|---|
| `gatstogo-mark-primary.svg` | **Default.** Emerald container, white cylinder. App icons, favicons, tablet home screen. |
| `gatstogo-mark-standalone.svg` | Emerald cylinder, no container. On light backgrounds where a container would be heavy. |
| `gatstogo-mark-reversed.svg` | White cylinder, no container. On dark or photographic backgrounds. |
| `gatstogo-mark-dark-container.svg` | Charcoal container. Dark UI surfaces. |
| `gatstogo-mark-alert.svg` | Unbalanced state. See below. |
| `gatstogo-mark-mono-black.svg` | Single-colour print, stamps, fax, embroidery. |
| `gatstogo-mark-mono-white.svg` | Single-colour reversed. |
| `gatstogo-lockup-light.svg` | Mark plus wordmark, for light backgrounds. |
| `gatstogo-lockup-dark.svg` | Mark plus wordmark, for dark backgrounds. |

### `png/` — for places that cannot take SVG

Mark at 1024, 512, 256, 192, 128, 64, 48, 32, 16 px, plus standalone and
reversed at 512, and both lockups at 1080 wide. All transparent where relevant.

### `favicon.ico`

Multi-resolution (16, 32, 48). Drop at the web root.

---

## Colours

| Name | Hex | Role in the mark |
|---|---|---|
| Deep Emerald | `#006B4D` | Container, or the cylinder body |
| Electric Mint | `#10B981` | Lower bar |
| Off-White | `#F8FAFC` | Upper bar on the standalone version |
| Slate Charcoal | `#0F172A` | Dark container, wordmark |
| Dark Amber | `#B45309` | Alert container |
| Flame Amber | `#F59E0B` | Alert lower bar |

---

## The alert variant

The lower bar runs short and the palette shifts to amber. Use it for:

- Reconciliation warnings on the owner dashboard
- Push notification badges when a day does not balance
- The terminal's offline-with-queued-fills state

Both bars are named paths — `bar-gas` and `bar-money` — so the unbalanced state
is a `width` and `fill` change in CSS, not a second asset:

```css
.mark--alert #bar-money { width: 21px; fill: #F59E0B; }
```

Do not use the alert variant as a general brand mark. It should only ever appear
when something genuinely needs attention, or it stops meaning anything.

---

## Rules

**Clear space.** Leave at least the width of the cylinder's collar (14 units at
the 120px master size) on every side. Nothing sits inside that.

**Minimum sizes.** 16px for the mark. 96px wide for the horizontal lockup — below
that, use the mark alone.

**The bars sit slightly above the vertical centre** of the cylinder body. This is
deliberate optical compensation. True centring reads as sagging. Keep the offset
if the mark is ever redrawn.

**Do not:** rotate it, add gradients or shadows, outline it, stretch it,
recolour it outside the palette above, or place the coloured versions on busy
photography — use `gatstogo-mark-reversed.svg` there instead.

---

## Wordmark typeface

The lockup SVGs reference `Archivo` with a system-sans fallback. Two things
before production:

1. Confirm the typeface. A grotesque with a distinctive lowercase `g` suits it —
   the name has two.
2. **Convert the wordmark to outlines** once confirmed, so it renders identically
   everywhere without a webfont dependency. Until then, the lockup PNGs will
   render in whatever fallback the exporting machine has.

The mark itself is pure vector shapes with no type, so it is safe to use
anywhere as-is.

---

## Web setup

Paths below match this repo (`/static/images/gatstogo-logo/...`). Prefer the
shared helpers in `web/templates/components/brand.templ` (`BrandFavicons`,
`BrandMark`) rather than hand-copying these tags.

```html
<link rel="icon" href="/static/images/gatstogo-logo/favicon.ico" sizes="any">
<link rel="icon" href="/static/images/gatstogo-logo/svg/gatstogo-mark-primary.svg" type="image/svg+xml">
<link rel="apple-touch-icon" href="/static/images/gatstogo-logo/png/gatstogo-mark-192.png">
```

| Surface | Asset | Typical CSS size |
|---|---|---|
| Marketing header / footer / 404 | `mark-primary.svg` | 26px |
| Login form (light) | `mark-primary.svg` | 32px |
| Login photo panel / terminal bar | `mark-reversed.svg` | 24–28px |
| Owner/admin rail + splash | `mark-dark-container.svg` | 32–48px |
| Favicon | `favicon.ico` + primary SVG + 192 PNG | browser |

For the attendant terminal PWA manifest, use the 192 and 512 PNGs and set
`"purpose": "any maskable"` — the emerald container already provides the safe
area Android needs.
