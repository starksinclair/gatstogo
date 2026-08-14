# GatsToGo — Production Build-Out Audit

**Scope:** everything built since the pre-build-out baseline (commit `aeb4ff5`, tagged `Baseline: import existing GatsToGo codebase pre-build-out`) through the current `HEAD`. 23 commits, 59 files touched, +9,413/−1,230 lines.

**What "verified" means in this document, specifically:** two different things, called out separately for every claim below —
- **Unit/integration-tested**: covered by `go test ./...` (41 tests, all passing, no live infrastructure required — miniredis stands in for Redis).
- **Live-verified**: actually exercised against a real running Postgres + Redis + Paystack-test-mode stack (via `podman compose`), by sending real HTTP requests and reading back real database rows — not mocked, not assumed. This is the higher bar, and it's what caught every bug listed in [§7](#7-bugs-found-and-fixed-during-live-verification).

Where something is neither, it's called out explicitly as unverified, not implied.

This document does not repeat the original 30-phase pre-build-out audit's findings (auth-less, RLS-inert, zero mutating routes) except as a "before" baseline for contrast. It also does not repeat `README.md`'s setup instructions — see that file to actually run this.

---

## 1. Executive Summary

The pre-build-out app was a read-only demo: no authentication anywhere, Row-Level Security policies that existed in the schema but were never actually activated (the DB roles/session variable they depend on were never used), and every mutating control in the UI (buy gas, set price, start shift, add staff, redeem a code) was wired to nothing.

That is no longer true. Every one of those write paths is now a real, database-backed, RLS-scoped, audit-logged HTTP handler, and — as of this session — every one of them has been exercised against a genuinely running stack, not just unit-tested. In the process, live testing caught **four real bugs that no amount of static review or mocked testing had found** (§7), including one that would have made the entire staff terminal fail to load in any real browser.

Headline numbers:
- **23 commits**, organized as 13 planned milestones (M0–M13) plus 5 post-hoc live-verification fixes.
- **16 new `internal/` source files** (plus 11 new test files) across **13 new packages**, plus **6 new `cmd/server/*.go`** handler files.
- **41 automated tests**, 0 failing.
- **9 templ pages** touched or added; the generated `_templ.go` companions regenerate clean.
- **Live database right now** (see §5 for detail): 2 plants, 5 users, 3 tickets (all paid and filled, 2 confirmed by the customer), 3 completed shifts, 8 cash movements, 22 audit log rows — all produced by actually using the app, not seeded.

---

## 2. Architecture At a Glance

| Layer | Technology | Notes |
| --- | --- | --- |
| Server | Go 1.26, `chi` v5 router | `cmd/server/*.go` |
| Templates | `templ` v0.3.1020 | `.templ` is source of truth; `_templ.go` is generated, never hand-edited |
| Database | PostgreSQL 16 via `pgx/v5` | Row-Level Security keyed off a per-transaction `current_plant_id()` GUC |
| DB roles | `gatstogo_app` (RLS-restricted) / `gatstogo_admin` (bypasses RLS) | Real, separate Postgres roles — not a shared superuser |
| Sessions | Redis via `go-redis/v9` | Also used for PIN rate-limiting and OTP codes |
| Passwords/PINs | bcrypt (`golang.org/x/crypto/bcrypt`) | Same hashing function for both |
| Payments | Paystack HTTP API, direct (`net/http`, no SDK) | Initialize, Verify, HMAC-SHA512 webhook signature |
| CSRF | HMAC-SHA256 (authenticated) / double-submit cookie (public forms) | `internal/csrf` |
| Multi-tenancy | Subdomain routing (`sunrise.localhost`) + `tenantdb.WithTenant` | See §4 |
| Containers | Docker Compose (also verified under `podman compose`, rootless) | `docker-compose.yaml` |

---

## 3. Complete File Inventory

### 3.1 New `internal/` packages (13)

| Package | File(s) | Purpose |
| --- | --- | --- |
| `tenantdb` | `tenantdb.go` | `Querier` interface (`Exec`/`Query`/`QueryRow`) satisfied as-is by `*pgxpool.Pool`, `*pgxpool.Conn`, `pgx.Tx`. `WithTenant(ctx, pool, plantID, fn)` opens a transaction, runs `SELECT set_config('app.current_plant_id', $1, true)`, runs `fn`, commits/rolls back. This is the one function every RLS-scoped query in the app goes through. |
| `session` | `session.go`, `cookie.go` | Redis-backed login sessions. `Data{UserID, Role, Name, PlantID, ShiftID, Phone, CreatedAt}`. Six roles: `owner`, `manager`, `cashier`, `operator`, `admin`, `customer`. Three TTLs: `OwnerTTL` (12h sliding), `StaffBackstopTTL` (24h leak-prevention ceiling — real expiry is shift-close), `ReceiptSessionTTL` (30m). `cookie.go` is the single choke point for cookie flags: host-only (no `Domain=`), `HttpOnly`, `SameSite=Lax`, `Secure` unless `APP_ENV=development`. |
| `auth` | `password.go` | `HashPassword`/`VerifyPassword` — bcrypt, shared by `users.password_hash` and `users.pin_hash`. |
| `csrf` | `csrf.go` | `Token`/`Verify` — HMAC-SHA256(server secret, session token), no extra storage needed. `NewPublicToken`/`VerifyPublic` — random double-submit-cookie token for forms with no session yet. |
| `audit` | `audit.go` | `Log(ctx, q, plantID, actorID, action, subject, detail)` — writes one `activity_log` row using the *same* querier/transaction as the surrounding business write, so they commit or roll back together. Every mutating handler in the app calls this. |
| `payments` | `paystack.go`, `live_test.go` | `Client.Initialize`, `Client.Verify`, `Client.VerifyWebhookSignature` (HMAC-SHA512). Direct HTTP calls, no third-party SDK. `live_test.go` hits the real Paystack API and is skipped unless `PAYSTACK_SECRET_KEY` is set. |
| `tickets` | `tickets.go` | The central ticket write-path package. `PurchaseParams`/`Purchase` is the one shared core both `CreatePending` (online checkout) and `CreatePaidCash` (terminal cash sale) build on. Also: `Redeem`, `Fill`, `MarkPaid` (idempotent), `Void`, `LookupPlantByReference`, `NormalizePhone` (canonicalizes any phone format to a comparable last-10-digits form), `ListByPhone`, `GetReceipt`, `Confirm`. |
| `shifts` | `shifts.go` | `PINLimiter` (Redis-backed: 5 attempts, 20s lockout), `ListStaff`, `StartOrResume`, `Close`, `IncrementTokensIssued`/`Returned`, `Summary`, `MostRecentOpenShiftID`, `RecordCashMovement`. |
| `prices` | `prices.go` | `Set` — inserts a new price row (prices are immutable history, never updated in place). |
| `staff` | `staff.go` | `Create` (PIN-based terminal staff: manager/cashier/operator), `SetActive`. |
| `plants` | `plants.go` | `Create` — one atomic transaction: reserved-slug check, plant insert, owner user insert, starting price insert, audit log. `SetStatus` — also atomic with its own audit log. |
| `receipts` | `receipts.go` | `CodeStore.RequestCode`/`VerifyCode` — 6-digit Redis-backed OTP, 10-minute TTL. Includes brute-force lockout (5 wrong guesses → 5-minute lock) and a 60-second resend cooldown. |
| `sms` | `sms.go` | `Sender` interface + `LoggingSender` (the only implementation — logs, never sends). |

### 3.2 New `cmd/server/*.go` files (6; `main.go` itself heavily modified, not new)

| File | Purpose |
| --- | --- |
| `auth.go` | Owner/manager login+logout, admin login+logout. |
| `tickets.go` | `buyGasSubmitHandler` (create ticket + Paystack Initialize), `ticketCallbackHandler` (browser return from checkout), `paystackWebhookHandler` (signature-verified async confirmation), `ticketVoidHandler`. |
| `terminal.go` | `terminalPageHandler`, `terminalPinHandler`, `terminalRedeemHandler`, `terminalFillHandler`, `terminalCashSaleHandler`, `terminalTokenReturnHandler`, `terminalShiftEndHandler`. |
| `owner_write.go` | `ownerSetPriceHandler`, `ownerCreateStaffHandler`, `ownerStaffStatusHandler`, `ownerCashMovementHandler`. |
| `admin_write.go` | `adminCreatePlantHandler`, `adminPlantStatusHandler`. |
| `receipts.go` | `receiptsPageHandler` (renders all 3 states of `/receipts`), `receiptsLookupHandler`, `receiptsVerifyHandler`, `receiptsConfirmHandler`. |

`main.go` gained: `Server` struct fields for every new dependency (Redis, Sessions, Paystack, PINLimiter, Receipts, SMS, NotificationEmail), all route registrations, `~13` `loadX` DB helper widenings from `*pgxpool.Pool` to `tenantdb.Querier`, and the `ownerDashboardHandler`/`customerHomeHandler` handlers themselves.

### 3.3 New/modified `internal/middleware/*.go`

| File | Purpose |
| --- | --- |
| `session.go` | `Actor` struct, `GetActor`. Four session-gating middlewares, each matching its surface's UX: `RequireOwnerSession` (redirect to `/owner/login`), `RequireAdminSession` (redirect to `/admin/login`), `RequireStaffSession` (JSON error — the terminal is a fetch()-driven SPA, not page navigation), `RequireReceiptSession` (redirect to `/receipts?error=session` — there's no separate login page since `/receipts` itself branches on session presence). |
| `csrf.go` | `CSRF` middleware — picks session-derived vs. double-submit check based on whether `GetActor(ctx)` is populated. `EnsurePublicCSRFCookie`. |
| `tenant.go` | *Pre-existing*, one-line change: the slug→plant lookup pool swapped from `AppDB` to `AdminDB` (it can't itself be RLS-scoped, since discovering the plant ID is the whole point of the lookup). |

### 3.4 Migrations

| File | Status | Purpose |
| --- | --- | --- |
| `0001_init_schema.up/down.sql` | Pre-existing, untouched | Full schema: `plants`, `reserved_slugs`, `users`, `prices`, `shifts`, `tickets`, `cash_movements`, `activity_log`, RLS policies, `gatstogo_app`/`gatstogo_admin` role definitions. |
| `0002_seed_data.up.sql` | Modified this session | Local dev/demo fixture data. **Originally shipped with literal placeholder password/PIN hashes that could never be used to log in** (see §7.3) — now real bcrypt hashes of documented dev credentials for an owner, a cashier, and (added this session) a platform admin. Explicitly marked "never run against a real production database." |
| `0003_role_credentials.up/down.sql` | New | `ALTER ROLE gatstogo_app/gatstogo_admin WITH PASSWORD :'app_password'/:'admin_password'`, using psql's safe `:'var'` substitution. |
| `migrate.sh` | New, then fixed | Idempotent runner (`schema_migrations` tracking table so re-running against an already-migrated volume is a safe no-op). **Shipped with a real bug** in its own idempotency check (§7.1), only caught once actually run. |

### 3.5 Templates (`web/templates/pages/*.templ`)

| File | Status | Purpose |
| --- | --- | --- |
| `customer_home.templ` | Heavily modified | Real buy-gas form (POST `/tickets`, CSRF, live price). `CustomerReceipts` rewritten from static fixture HTML to `ReceiptsViewData`-driven real states (lookup / verify / list+detail). |
| `owner_login.templ` | New | Phone+password login. |
| `admin_login.templ` | New | Phone+password login (platform-wide). |
| `owner_admin.templ` | Heavily modified | Owner dashboard + admin console. Every "UI only" badge from the pre-build-out version replaced with a real form: set price, create/deactivate staff, record cash movements, void a ticket, create a plant, change a plant's status. |
| `terminal.templ` | Rewritten (~500 lines of embedded JS), then twice bug-fixed live | Real `fetch()`-driven state machine replacing a hardcoded local-state demo. See §7.2 and §7.4 for the two real bugs found here. |
| `ticket_confirmation.templ` | New | Post-payment confirmation page (shows the redemption code). |

Untouched, pre-existing, and **not wired into any route** (dead code inherited from before this build-out, left alone): `internal/config/dependencies.go`, `internal/handlers/attendant/attendant.go`, `web/templates/pages/dashboard.templ`. `main.go` still has them commented out (`//s.Router.Get("/", attendant.Test)` etc.) rather than referencing them.

---

## 4. Route Map

Every route below was confirmed by grepping the actual `chi` route registrations in `main.go` — not reconstructed from memory.

### Tenant-scoped (require a plant subdomain host, e.g. `sunrise.localhost`)

| Route | Method | Auth |
| --- | --- | --- |
| `/`, `/home` | GET | none |
| `/tickets` | POST | none (public CSRF) |
| `/tickets/{reference}/callback` | GET | none |
| `/tickets/{reference}/void` | POST | owner/manager session |
| `/tickets/{reference}/confirm` | POST | receipts session |
| `/receipts` | GET | none (soft session check inside the handler) |
| `/receipts/lookup`, `/receipts/verify` | POST | none (public CSRF) |
| `/terminal` | GET | none |
| `/terminal/pin` | POST | none (public CSRF) |
| `/terminal/redeem`, `/terminal/fill`, `/terminal/cash-sale`, `/terminal/token-return`, `/terminal/shift/end` | POST | staff session |
| `/owner/login` | GET/POST | none (public CSRF on POST) |
| `/owner`, `/owner/dashboard` | GET | owner/manager session |
| `/owner/logout`, `/owner/prices`, `/owner/staff`, `/owner/staff/{id}/status`, `/owner/cash-movements` | POST | owner/manager session |
| `/home/summary` | GET | none |

### Platform (outside the tenant middleware)

| Route | Method | Auth |
| --- | --- | --- |
| `/admin/login` | GET/POST | none (public CSRF on POST) |
| `/admin` | GET | admin session |
| `/admin/logout`, `/admin/plants`, `/admin/plants/{id}/status` | POST | admin session |
| `/webhooks/paystack` | POST | none — HMAC-SHA512 signature is the authenticity check, not a session |

---

## 5. Live-Verified End-to-End Flows

Everything in this section was run against a real `podman compose` stack (Postgres 16, Redis 7, the actual app binary via `air`) during this session — real HTTP requests, real database writes, confirmed by reading the rows back. This is not a description of what the code *should* do; it's what it was watched doing.

### 5.1 Authentication

| Flow | Result |
| --- | --- |
| Owner login (`08012345678` / `sunrise-owner-dev`) | bcrypt verify succeeded, real 12h session minted, dashboard rendered with live data (`John Owner`, `Sunrise Gas Plant`, live price) |
| Admin login (`08000000000` / `platform-admin-dev`) | Same, platform-wide session (no `plant_id`) |
| Terminal PIN login, cashier (Mary, `1234`) | bcrypt PIN verify, real shift opened, `shift_id` returned |
| Terminal PIN login, operator (Chidi, `5678`) | Same; confirmed operator's UI correctly shows fill-only (no cash tab) |
| Wrong-port access (`localhost:8080` instead of `:18080`) | Diagnosed live during this session — a genuine user-facing symptom (silent failure, zero server-side log trace) traced to a local port collision, not an app bug |

### 5.2 Tenant isolation (RLS)

Connected directly as `gatstogo_app` (the restricted role) with **no** `current_plant_id` set: `SELECT count(*) FROM tickets` returned **0**, not the true row count. RLS is actually enforced by Postgres, not just declared in the schema.

### 5.3 Migrations

Ran against a **fresh volume** (`podman compose down -v` then `up`) twice during this session. Second run confirmed: `0001`/`0002`/`0003` apply cleanly, `schema_migrations` correctly tracks them, and a subsequent no-op re-run correctly skips already-applied versions.

### 5.4 Payments

`POST /tickets` (₦5,000, transfer channel) → real `tickets` row created (`pending`) → live call to Paystack's Initialize Transaction API with the real test secret key → real `https://checkout.paystack.com/...` URL returned and the browser redirected to it. Separately, the customer's own real purchase during this session (₦10,000, channel `transfer`) went all the way through: created → paid (via the browser-return callback path, `GET /tickets/{reference}/callback`, which calls Paystack's Verify API) → redeemed at the terminal → filled → confirmed by the customer via the receipts flow.

**Not live-verified:** the webhook path (`POST /webhooks/paystack`). It needs Paystack's servers to reach a public URL, which nothing available in this session had. The callback path above is the fallback specifically for this reason, and it has been exercised for real — but the webhook itself, and its HMAC-SHA512 signature check against a genuine Paystack-signed request, has only been unit-tested (`internal/payments/paystack_test.go`), not run against a live delivery.

### 5.5 Staff terminal — full loop, both cashier and operator

Cashier (Mary): PIN login → cash sale (₦18,750 for 12.5kg, token issued) → redeem the code → fill → **shift end** (found broken, fixed — see §7.4 — then re-verified working: `opening_cash_kobo`/`closing_cash_kobo` both recorded correctly, shift closed).

Operator (Chidi): PIN login → fill-only UI confirmed (no cash tab rendered) → shift end (same bug, same fix, confirmed working: opening/closing cash both ₦13,000, matching since operators never touch cash).

Current live shift data (queried directly from the running database):

```
     name      | opening_cash | closing_cash | tokens_issued | tokens_returned | closed
----------------+-------------+--------------+----------------+-----------------+--------
 Mary Attendant |     ₦20,000 |      ₦25,000 |              0 |                  |  yes
 Mary Attendant |     ₦20,000 |      ₦22,300 |              1 |                1 |  yes
 Chidi Operator |     ₦13,000 |      ₦13,000 |              0 |                  |  yes
```

### 5.6 Owner write paths

Set price (₦1,550/kg) → reflected immediately on the dashboard and in the next ticket's rate snapshot. Create staff (Chidi Operator) → appeared in the staff table, could immediately log into the terminal. Record a cash movement (₦5,000 deposit) → required (and correctly refused without) an open shift; once one existed, recorded and visible on the dashboard. Void a ticket → ticket flipped to `voided`.

### 5.7 Admin write paths

Create a plant (`coastal`, "Coastal Gas Plant", owner "Amaka Owner") → new tenant subdomain (`coastal.localhost`) immediately live with the correct branding and starting price; the new owner could immediately log in with the password set at creation time. Suspend the plant → tenant site immediately started 404ing. Reactivate → immediately restored.

### 5.8 Customer receipts

Phone lookup → OTP generated and logged (never actually sent — see §8, "No real SMS delivery") → verify → real Redis-backed session minted (`RoleCustomer`) → `/receipts` correctly switched from the anonymous phone-entry form to the authenticated ticket list → detail view for a filled ticket showed "Ready to confirm" → **Confirm received** → status flipped to `Received` with a real `confirmed_at` timestamp, immediately reflected on re-fetch.

### 5.9 Activity/audit trail

Both the owner dashboard's "Activity" tab and the admin console's "Platform activity" tab were checked against the live `activity_log` table (22 rows at last count: `ticket.created`, `ticket.paid`, `ticket.filled`, `ticket.confirmed`, `ticket.voided`, `ticket.cash_sale`, `shift.opened`, `shift.closed`, `cash.recorded`, `staff.created`, `price.set`, `plant.created` all present). Both render real data, correctly scoped (owner sees only their plant's 6 most recent; admin sees the platform's 8 most recent, cross-tenant), no discrepancies found.

---

## 6. Automated Test Coverage

`go build ./...`, `go vet ./...`, and `go test ./...` all pass clean as of the last commit. 41 tests, 0 failures, 1 intentional skip (the live Paystack test, gated on `PAYSTACK_SECRET_KEY`).

| Package | Tests | What's covered |
| --- | --- | --- |
| `internal/audit` | 2 | Log writes, nil actor/detail handling (including a nil-interface footgun caught and fixed mid-build) |
| `internal/auth` | 3 | bcrypt hash/verify round-trip, wrong password, malformed hash |
| `internal/csrf` | 3 | Session-derived token verify, double-submit verify, tamper detection |
| `internal/payments` | 4 (+1 skipped) | Initialize/Verify request shape and error handling against a mock HTTP server; webhook signature verification; `TestLiveInitializeAndVerify` against the real API (skipped without a key, confirmed passing earlier this session when one was set) |
| `internal/plants` | 5 | Slug validation, hex color validation/defaults — deliberately validation-only (no DB) since `Create`/`SetStatus` need a real transaction |
| `internal/prices` | 1 | Rejects caller-supplied `effective_from` |
| `internal/receipts` | 9 | OTP request/verify round-trip, one-time consumption, wrong-code-not-consumed, per-phone/per-plant isolation, brute-force lockout, resend cooldown — all against `miniredis` |
| `internal/shifts` | 3 | `PINLimiter` attempt counting and lockout, against `miniredis` |
| `internal/staff` | 2 | PIN format validation |
| `internal/tickets` | 9 | Reference/code generation and sanitization, the shared `Purchase` core (both `CreatePending` and `CreatePaidCash` verified to go through it via a fake `Querier` capturing actual SQL args), amount/grams round-trip math, `NormalizePhone` |

No test requires a live Postgres or Redis instance — `miniredis` (an in-memory Redis-protocol server) stands in wherever Redis is needed, and DB-dependent packages either use a fake `Querier` capturing arguments or restrict their tests to pure validation logic.

---

## 7. Bugs Found and Fixed During Live Verification

This is the section that justifies "run it, don't just review it." None of these four were caught by static review, `go vet`, or the 41 unit tests above — all four were only found by actually operating the running application.

### 7.1 `migrate.sh`: `psql -c` silently skips variable substitution

**Symptom:** the very first migration run failed with `ERROR: syntax error at or near ":"` on `SELECT 1 FROM schema_migrations WHERE version = :'version';`.

**Root cause:** `psql -c "...:'version'..."` sends the command string to the server close to verbatim — psql's own variable-interpolation pass runs for script/stdin input (`-f`, or piped stdin) but not for `-c`. Confirmed directly: `-v foo=bar -c "\echo :foo"` prints `bar` (the meta-command layer sees the variable), but `-v foo=bar -c "SELECT :foo;"` fails the same way (the SQL sent to the server does not).

**Fix:** switched both affected statements (the idempotency check and the version-insert) from `-c "..."` to heredocs on stdin. `-f "$file"` was never affected — it already went through stdin/script-parsed input.

**Why static review missed it:** no Docker/Postgres access existed anywhere earlier in this build-out; this script had only ever been read, never executed, until this session.

### 7.2 The staff terminal never actually booted in a real browser

**Symptom:** none visible without opening a real browser — this was caught by rendering `/terminal` directly and inspecting the response, not by a user report.

**Root cause:** `terminal.templ` had:
```html
<script type="application/json" id="terminal-init">{ initJSON }</script>
```
`templ` does not run Go-expression interpolation inside a `<script>` element's body — script (and style) contents are treated as opaque literal text, by design (the same reason the ~500-line inline JS block full of real `{ }` object literals elsewhere on the same page correctly renders as literal JS). This one line relied on the opposite behavior. The page was shipping the eight literal characters `{ initJSON }` to the browser. The terminal's own JS does `JSON.parse(document.getElementById("terminal-init").textContent)` as its very first action on load — so every real terminal page load would throw immediately: no staff list, no CSRF token, no live price, nothing would ever render.

**Fix:** used templ's own builtin for exactly this (`templ.JSONScript`, which does its own `json.Marshal` and safe script-tag rendering) instead of hand-rolling one: `@templ.JSONScript("terminal-init", init)`. `StaffTerminal`'s second parameter changed from a pre-marshaled `string` to `any`, passed straight through from `cmd/server/terminal.go`.

**Why testing missed it:** the terminal's JS had been tested with a hand-rolled DOM/fetch mock harness (`terminal_sim.js`, built earlier in this project specifically because no browser was available in the sandbox) that supplied a mocked `init` object directly — bypassing templ's actual renderer entirely, so it had no way to notice templ never filled in the real one. Every other `.templ` file was grepped afterward for the same pattern (a standalone `<script>` whose entire body is one `{ expr }`); this was the only occurrence.

### 7.3 Seed data credentials were unusable placeholders

**Symptom:** nobody could log in as the seeded owner or cashier — every attempt failed with "incorrect password"/"incorrect PIN".

**Root cause:** `0002_seed_data.up.sql`'s `password_hash`/`pin_hash` columns were literal placeholder text (`'$2a$10$examplehashedpassword'`, `'$2a$10$examplehashedpin'`) — never real bcrypt output, so no plaintext could ever match them. Also: no admin-role user existed in seed data at all, meaning `/admin` was unreachable by anyone on a fresh database.

**Fix:** generated real bcrypt hashes (via `golang.org/x/crypto/bcrypt`, the same function `internal/auth.HashPassword` uses) for three documented dev logins — owner, cashier, and a newly-added platform admin — and replaced the placeholders. Credentials are intentionally not secret (this migration is explicitly marked dev/demo-only, never for a real production database) and are documented in `README.md`.

**Flagged, not fixed:** there is still no bootstrap mechanism for creating the *first* admin user on a real production database — this seed row only helps local dev/demo. See §8.

### 7.4 Operator role couldn't end their own shift

**Symptom:** reported directly during manual testing — Chidi (operator) tapping "End shift" did nothing.

**Root cause:** `terminal.templ`'s `setTab()`:
```js
function setTab(tab) {
    if (state.config === "operator" && tab !== "fill") return;
    ...
```
The guard's intent was to keep an operator off the cash-sale tab (operators have no cash-handling permission). Written as `tab !== "fill"`, it blocked *everything* else too, including `"end"` — the header's "End shift" button and the End-shift tab both route through `setTab("end")`, silently refused for operators the same as `setTab("cash")` would have been. Cashiers and managers were unaffected (the guard only applies when `state.config === "operator"`).

**Fix:** narrowed the guard to what it actually needs to block: `if (state.config === "operator" && tab === "cash") return;`.

**Why testing missed it:** this is client-side tab-switching logic that only manifests through an actual click — every prior verification pass in this session (including §5's) drove the terminal's API endpoints directly via HTTP requests, which structurally cannot exercise `setTab()` at all.

---

## 8. Known Gaps (Explicit, Not Silently Assumed Away)

| Gap | Detail |
| --- | --- |
| **No real SMS delivery** | `internal/sms.LoggingSender` is the only `Sender` — receipts OTP codes are written to the server log, never texted. Confirmed live: codes only ever appeared in `podman logs gatstogo-app`, never reached an actual phone. Swapping in a real provider (Termii, Africa's Talking, Twilio) is meant to be a one-file change (implement `sms.Sender`), but that integration itself doesn't exist yet. |
| **Paystack webhook path unverified live** | See §5.4. Initialize and Verify are confirmed live; the webhook needs a publicly reachable URL nothing in this session had. |
| **No production admin-bootstrap path** | `0002_seed_data.up.sql` is explicitly dev/demo-only. There is no CLI tool, migration, or documented manual step for provisioning the very first admin on a real deployment — every `/admin` route requires an existing admin session, and nothing creates one outside local seed data. |
| **No per-token identity** | A terminal "token number" shown to a cashier is just the running `tokens_issued` counter for that shift, not a row in its own table. Documented in `internal/shifts/shifts.go`. |
| **No scale integration** | `net_grams`/`empty_weight_grams`/`filled_weight_grams` are never set — there's no weighing-scale input step. Filled tickets show "awaiting fill" for net weight indefinitely. Documented in `internal/tickets/tickets.go`. |
| **Owner cash movements can't target a specific shift** | `RecordCashMovement` always attaches to the plant's most-recently-opened shift; there's no shift-picker if more than one is open. Documented in `internal/shifts/shifts.go`. |
| **Staff promotion to owner-dashboard access is manual** | The owner dashboard's "Add staff" form only ever sets `pin_hash` (terminal access). Granting a promoted staff member `password_hash`-based owner-dashboard login is a separate, not-yet-built step. Documented in `internal/staff/staff.go`. |
| **Default secrets are placeholders** | `docker-compose.yaml`/`.env.example`'s role passwords, `SESSION_HMAC_SECRET`, and the Postgres bootstrap superuser password are all local-dev placeholders — must be rotated before any real deployment. |

**Not a gap, confirmed complete:** `go.mod`/`go.sum` — `go mod tidy` was run with real module-proxy network access this session and made zero changes.

---

## 9. Security Posture Summary

- **Tenant isolation**: Postgres Row-Level Security, keyed off `current_plant_id()`, set per-transaction by `tenantdb.WithTenant`. Confirmed live (§5.2) that the restricted role sees zero rows without it — this is enforced by Postgres itself, not just application-layer filtering.
- **Two DB roles**: `gatstogo_app` (RLS-restricted, all normal traffic) and `gatstogo_admin` (bypasses RLS, only for genuinely cross-tenant operations: the slug→plant lookup, plant onboarding, the Paystack webhook's reference lookup). The app actually connects as these roles, not a shared superuser.
- **Passwords/PINs**: bcrypt, `DefaultCost`, never compared as plaintext.
- **Sessions**: Redis-backed, random 256-bit tokens, host-only cookies (no `Domain=`, so a session minted on one tenant subdomain structurally cannot be sent to another), `HttpOnly`, `SameSite=Lax`, `Secure` unless explicitly in development mode.
- **CSRF**: every mutating route is covered — session-derived HMAC token for authenticated forms, double-submit cookie for public ones. The one deliberate exception is `POST /webhooks/paystack`, which has no session/cookies to begin with and instead authenticates via `X-Paystack-Signature` (HMAC-SHA512 over the raw, pre-parsed request body).
- **Rate limiting**: PIN attempts (5 tries / 20s lockout) and receipts OTP verification (5 tries / 5-minute lockout, plus a 60-second resend cooldown) are both Redis-enforced, closing brute-force paths a naive implementation would leave open.
- **Audit trail**: every mutating handler calls `audit.Log` inside the same transaction as its business write — confirmed live (§5.9) with 22 real rows covering every write path exercised this session.

---

## 10. Recommended Next Steps

In priority order:

1. **Decide on SMS provider** and wire it in behind the existing `sms.Sender` interface — the receipts flow is otherwise complete but non-functional for real users without this.
2. **Decide on admin bootstrap mechanism** for real deployments (CLI tool vs. documented manual SQL vs. an env-var-driven first-run bootstrap) — currently a hard blocker for standing up a real instance.
3. **Verify the Paystack webhook live**, once deployed somewhere with a public URL (or via a tunnel like ngrok pointed at a local instance).
4. **Rotate every default credential** in `docker-compose.yaml`/`.env.example` before any real deployment.
5. Continue exercising the UI by hand where it's cheap to do so — three of this session's four bugs (§7.2, §7.4, and indirectly §7.3) were only found that way, not through API-level testing.

---

## Appendix: Full Commit Log

In order, oldest first, from the baseline (`aeb4ff5`) to `HEAD`:

```
5d32850 M0: tooling and hygiene for the production build-out
ce65317 go.mod: correct a-h/templ to a direct dependency
f566f17 M2: internal/tenantdb + tenant-scoped queries
011a589 M3: real credentials for gatstogo_app/gatstogo_admin, migrations actually run
62bd3b6 M4: Redis-backed login sessions
4f3e5d8 M5: real login for owner/manager and admin, GET /owner and GET /admin gated
0a04963 M6: CSRF protection, plus the sign-out UI M5 shipped without
36ffb12 M7: activity_log audit helper
61998a8 M8: real ticket creation + Paystack payment integration
2e973d1 Fix a real bug caught by testing against the live Paystack API: rejected placeholder email domain
0a42808 Harden Paystack integration against official docs, not just training knowledge
4838c57 M9a: staff terminal backend -- PIN login, shifts, cash sales, redemption
2a95cc6 Unify ticket creation: online checkout and terminal cash sale now share one core
c60423e M9b: staff terminal frontend now calls the real backend
e4d6497 M10: real price-setting and staff management for the owner dashboard
1d74197 M11: real plant onboarding for the admin console, reserved slugs finally enforced
6495ca9 M12: real customer receipts lookup, phone-verified confirmation, SMS interface
7eb649c M13: rewrite README for the production build-out (M0-M12)
e89c088 Fix migrate.sh: psql -c doesn't run :'var' substitution, stdin/-f does
49e0af6 Fix a critical bug: the staff terminal never actually booted in a real browser
b64fe5e Add a seeded platform admin (dev/demo only) so /admin is actually reachable
ad569ac README: document seeded dev logins, update Current Status with what actually got live-verified
7403e92 Fix operator role unable to end their own shift
```

(`M1` — adding `go-redis`/`golang.org/x/crypto` to `go.mod` — landed folded into `M4`'s and `M5`'s own commits rather than as its own; `ce65317` is a one-line follow-up fixing `a-h/templ`'s `go.mod` classification, not a milestone itself.)
