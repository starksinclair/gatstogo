# GatsToGo

GatsToGo is a multi-tenant LPG plant web app. Each gas plant gets a white-labelled customer site, while GatsToGo provides internal owner, admin, and staff terminal surfaces for operating the plant.

The server is Go with server-rendered `templ` pages, PostgreSQL (with Row-Level Security enforcing tenant isolation) via `pgx`, and Redis-backed sessions. Authentication, payments (Paystack), shifts/cash handling, and the customer receipts flow are real, database-backed write paths, not UI mockups.

## What Is Here

- Customer site: buy gas with no signup, pay by transfer/card/USSD via Paystack, get a redemption code
- Customer receipts: phone number + SMS one-time code, then a real ticket list, detail view, and "confirm received" action
- Staff terminal: PIN login tied to a shift, code redemption, fill confirmation, cash sales, token returns, shift close
- Owner dashboard: live ticket/shift/cash data, set prices, create/deactivate staff, record cash movements, void tickets
- Admin console: platform-wide plant onboarding (enforcing `reserved_slugs`), plant status changes
- PostgreSQL schema for tenants, users, prices, shifts, tickets, cash movements, reserved slugs, and activity logs, with RLS policies and a restricted (`gatstogo_app`) vs. bypass (`gatstogo_admin`) role split
- Brand foundation prototypes in `gatstogo-brand-foundation/project`

## Tech Stack

- Go `1.26`
- Chi router
- `templ` for server-rendered HTML
- PostgreSQL via `pgx`, with Row-Level Security for tenant isolation
- Redis (`go-redis/v9`) for login sessions, PIN/OTP rate limiting, and one-time codes
- `bcrypt` for password and PIN hashing
- Paystack for payments (Initialize/Verify Transaction, HMAC-SHA512-verified webhooks) -- called directly over `net/http`, no SDK
- Docker Compose for local app/database/redis setup
- Air for hot reload in the development container
- Alpine is used on the customer buy form
- Vanilla JavaScript drives the owner/admin tabs and the staff terminal (a real `fetch()`-driven state machine, not local mock state)
- Static CSS files are served from `web/static/css`

## Project Structure

```text
cmd/server/*.go                    Go server: routing, DB loaders, and every HTTP handler
internal/tenantdb                  Querier interface + WithTenant (RLS-scoped transaction helper)
internal/session                   Redis-backed login sessions + cookie handling
internal/auth                      Password hashing (bcrypt)
internal/csrf                      CSRF tokens: session-derived (authenticated) and double-submit (public forms)
internal/middleware                Tenant lookup + RequireOwnerSession/RequireAdminSession/RequireStaffSession/RequireReceiptSession
internal/audit                     activity_log write helper
internal/payments                  Paystack client (Initialize, Verify, webhook signature verification)
internal/tickets                   Ticket creation (shared by online checkout and terminal cash sales), redemption, fill, void, receipts lookup
internal/shifts                    Shift open/close, PIN rate limiting, cash movements
internal/prices, internal/staff, internal/plants   Owner/admin write paths (prices, staff, plant onboarding)
internal/receipts                  Customer receipts one-time code issuance/verification
internal/sms                       SMS Sender interface (LoggingSender only -- see Notes)
internal/domain                    Database model structs
migrations                         Database schema, seed data, and role-credential SQL
web/templates                      templ source files
web/templates/**/*_templ.go        generated templ files (do not edit by hand)
web/static/css                     page CSS
gatstogo-brand-foundation/project  design prototypes
```

Do not edit generated `*_templ.go` files by hand. Edit the `.templ` files, then regenerate.

## Routes

Tenant-scoped routes require a plant subdomain-style host, because the tenant middleware reads the plant slug from the first host segment:

```text
http://sunrise.localhost:8080/
```

### Tenant Pages (require a plant host)

| Route | Method | Auth |
| --- | --- | --- |
| `/`, `/home` | GET | none |
| `/tickets` | POST | none (public CSRF) -- creates a ticket, starts Paystack checkout |
| `/tickets/{reference}/callback` | GET | none -- browser return from Paystack, verify fallback |
| `/tickets/{reference}/void` | POST | owner/manager session |
| `/tickets/{reference}/confirm` | POST | receipts session |
| `/receipts` | GET | none -- phone entry, or the ticket list if a receipts session cookie is present |
| `/receipts/lookup` | POST | none (public CSRF) -- sends a one-time SMS code |
| `/receipts/verify` | POST | none (public CSRF) -- verifies the code, mints a receipts session |
| `/terminal` | GET | none -- the terminal UI itself; the PIN screen gates real actions |
| `/terminal/pin` | POST | none (public CSRF) -- PIN login, opens/resumes a shift |
| `/terminal/redeem`, `/terminal/fill`, `/terminal/cash-sale`, `/terminal/token-return`, `/terminal/shift/end` | POST | staff session (cashier/operator/manager, open shift) |
| `/owner/login` | GET/POST | none (public CSRF on POST) |
| `/owner`, `/owner/dashboard` | GET | owner/manager session |
| `/owner/logout` | POST | owner/manager session |
| `/owner/prices`, `/owner/staff`, `/owner/staff/{id}/status`, `/owner/cash-movements` | POST | owner/manager session |
| `/home/summary` | GET | none -- small plant summary component |

### Platform Routes (outside the tenant middleware)

| Route | Method | Auth |
| --- | --- | --- |
| `/admin/login` | GET/POST | none (public CSRF on POST) |
| `/admin` | GET | admin session |
| `/admin/logout` | POST | admin session |
| `/admin/plants` | POST | admin session -- create a plant (validated against `reserved_slugs`) |
| `/admin/plants/{id}/status` | POST | admin session |
| `/webhooks/paystack` | POST | none -- server-to-server; authenticity comes from the `X-Paystack-Signature` HMAC, not a session/CSRF token |

Every authenticated route sits behind a session (`internal/session`, Redis-backed) and, for mutating requests, CSRF (`internal/csrf`): authenticated forms get a token derived from the session itself; public forms (buy gas, receipts lookup/verify, every `*/login`) get a double-submit cookie token instead, since no session exists yet at that point.

## Current Status

Authentication, tenant isolation (RLS), sessions, CSRF, and every write path listed above are real and database-backed -- this is no longer a UI-only prototype. What's still worth knowing before treating this as fully production-ready:

- **Paystack and SMS have not been exercised against live traffic from this environment.** The Paystack integration (`internal/payments`) has been verified against the real Paystack API using a test secret key (`internal/payments/live_test.go`, skipped unless `PAYSTACK_SECRET_KEY` is set) and cross-checked against Paystack's own documentation, but a full live checkout -> webhook -> terminal redemption run needs to happen in an environment with real network access and a real (even if test-mode) Paystack account.
- **SMS is not actually sent yet.** `internal/sms.LoggingSender` is the only `Sender` implementation -- receipts one-time codes are written to the server log, not delivered to a phone. Swapping in a real provider (Termii, Africa's Talking, Twilio, etc.) is meant to be a one-file change (implement `sms.Sender`), but that provider integration itself hasn't been built.
- **`go.sum` may need regenerating.** This build-out added `redis/go-redis/v9`, `golang.org/x/crypto` (bcrypt), and, test-only, `alicebob/miniredis/v2` to `go.mod`. If `go.sum` wasn't generated with real module-proxy network access, run `go mod tidy` once before deploying.
- **Default credentials in `docker-compose.yaml` and `.env.example` are placeholders.** Rotate `GATSTOGO_APP_ROLE_PASSWORD`, `GATSTOGO_ADMIN_ROLE_PASSWORD`, `SESSION_HMAC_SECRET`, and the Postgres bootstrap superuser password for any real deployment; none of them are safe to use as-is in production.

## Environment Variables

See `.env.example` for the full, commented list (copy it to `.env`). The server reads:

```env
# Core
APP_PORT=8080
APP_ENV=development          # anything else enables the cookie Secure flag
APP_BASE_URL=http://localhost:8080

# Database -- gatstogo_app is RLS-restricted (normal tenant traffic),
# gatstogo_admin bypasses RLS (platform-level reads/writes)
APP_DATABASE_URL=postgres://gatstogo_app:...@host:5432/gatstogo?sslmode=disable
ADMIN_DATABASE_URL=postgres://gatstogo_admin:...@host:5432/gatstogo?sslmode=disable
# In Docker Compose, the two role passwords above are actually set here and
# applied by migrations/0003_role_credentials.up.sql:
GATSTOGO_APP_ROLE_PASSWORD=
GATSTOGO_ADMIN_ROLE_PASSWORD=

# Sessions (Redis)
REDIS_URL=redis://localhost:6379/0
# HMAC key for CSRF tokens. Generate with: openssl rand -base64 32
SESSION_HMAC_SECRET=

# Payments (Paystack) -- https://dashboard.paystack.com/#/settings/developers
# Required. Also used to verify the X-Paystack-Signature webhook header.
PAYSTACK_SECRET_KEY=
PAYSTACK_PUBLIC_KEY=
# Optional -- real inbox Paystack's Initialize API email is built from
# (Gmail-style "+tag" addressing per ticket). Defaults to gatstogofficial@gmail.com.
TICKET_NOTIFICATION_EMAIL=

# SMS -- no real provider wired up yet, see Current Status above.
SMS_PROVIDER=logging
```

`APP_DATABASE_URL`, `ADMIN_DATABASE_URL`, `SESSION_HMAC_SECRET`, and `PAYSTACK_SECRET_KEY` are required -- the server fails fast at startup if any is missing or malformed, rather than starting in a half-configured state.

## Run With Docker

From the project root, copy the env template and fill in real values (at minimum: role passwords, `SESSION_HMAC_SECRET`, `PAYSTACK_SECRET_KEY`):

```bash
cp .env.example .env
# edit .env
docker compose up --build
```

This brings up Postgres, an idempotent `migrate` service (applies `migrations/*.up.sql`, tracked in a `schema_migrations` table, and sets the `gatstogo_app`/`gatstogo_admin` role passwords), Redis, and the app -- `app` waits on `postgres` (healthy), `migrate` (completed), and `redis` (healthy) before starting.

The app listens on:

```text
http://localhost:8080
```

For tenant pages, use a plant subdomain host. The seed data creates a `sunrise` plant, so use:

```text
http://sunrise.localhost:8080/
http://sunrise.localhost:8080/terminal
http://sunrise.localhost:8080/owner/login
```

Admin console:

```text
http://localhost:8080/admin/login
```

Optional Adminer database UI is available through the `tools` profile:

```bash
docker compose --profile tools up adminer
```

Then open:

```text
http://localhost:8083
```

## Run Locally Without Docker

1. Start PostgreSQL and Redis.

2. Create a database:

```bash
createdb gatstogo
```

3. Apply the schema, seed data, and role-credential migration in order:

```bash
export DATABASE_URL="postgres://gatstogo:gatstogo_secret@localhost:5432/gatstogo?sslmode=disable"
psql "$DATABASE_URL" -f migrations/0001_init_schema.up.sql
psql "$DATABASE_URL" -f migrations/0002_seed_data.up.sql
psql "$DATABASE_URL" -v app_password='dev_app_password' -v admin_password='dev_admin_password' \
  -f migrations/0003_role_credentials.up.sql
```

(Or use `migrations/migrate.sh`, the same idempotent runner the Docker `migrate` service uses -- it tracks applied migrations in `schema_migrations`, so re-running it is safe.)

4. Create a `.env` file (see the full list above / `.env.example`):

```env
APP_PORT=8080
APP_DATABASE_URL=postgres://gatstogo_app:dev_app_password@localhost:5432/gatstogo?sslmode=disable
ADMIN_DATABASE_URL=postgres://gatstogo_admin:dev_admin_password@localhost:5432/gatstogo?sslmode=disable
REDIS_URL=redis://localhost:6379/0
SESSION_HMAC_SECRET=<openssl rand -base64 32>
PAYSTACK_SECRET_KEY=<your test secret key>
```

5. Generate templates:

```bash
go run github.com/a-h/templ/cmd/templ@v0.3.1020 generate
```

6. Run the server:

```bash
go run ./cmd/server
```

## Development Workflow

After editing any `.templ` file, regenerate:

```bash
go run github.com/a-h/templ/cmd/templ@v0.3.1020 generate
```

Run the full check before committing:

```bash
go build ./...
go vet ./...
go test ./...
```

Most packages' tests run against `miniredis` (an in-memory Redis-protocol server) rather than a live Redis instance, so `go test ./...` works without Docker or a running Redis daemon. `internal/payments/live_test.go` is the one exception -- it's skipped unless `PAYSTACK_SECRET_KEY` is set, since it makes real calls to the Paystack API.

## Static Assets

Static files are served from:

```text
/static/*
```

The current CSS files are:

- `web/static/css/base.css`
- `web/static/css/customer-home.css`
- `web/static/css/owner-admin.css`
- `web/static/css/terminal.css`

## Database Tables

The initial schema creates:

- `plants` - tenant profile, branding, status, domains
- `reserved_slugs` - protected subdomains, enforced at plant creation (`/admin/plants`) and at tenant resolution
- `users` - owners, managers, cashiers, operators, admins (bcrypt-hashed `password_hash`/PIN)
- `prices` - immutable price history
- `shifts` - shift open/close records and drawer state
- `tickets` - customer purchases, payment state, fill state, receipt proof
- `cash_movements` - cash drawer movements
- `activity_log` - audit trail, written by every mutating handler alongside its business write (same transaction)

Row-Level Security policies enforce tenant isolation on every tenant-scoped table, keyed off a `current_plant_id()` session variable that `internal/tenantdb.WithTenant` sets per-transaction. The schema also defines `gatstogo_app` (RLS-restricted, normal traffic) and `gatstogo_admin` (bypasses RLS, platform-level) Postgres roles, and the app actually connects as those roles rather than a shared superuser.

## Tenant Resolution

Tenant pages use the first part of the host as the plant slug:

```text
sunrise.localhost:8080 -> sunrise
```

The slug -> plant lookup runs on the `gatstogo_admin` connection (it's how a plant id is discovered in the first place, so it can't itself be RLS-scoped); every query after that runs tenant-scoped through `internal/tenantdb.WithTenant` on the restricted `gatstogo_app` connection. Reserved/system slugs (seeded in `reserved_slugs`, plus a small hardcoded list such as `www`, `admin`, `api`) are rejected both by the tenant middleware and by plant creation.

## Design References

The implementation is based on the design prototypes in:

```text
gatstogo-brand-foundation/project/
```

Useful files:

- `GatsToGo Customer Site.dc.html`
- `GatsToGo Owner Dashboard.dc.html`
- `GatsToGo Admin Console.dc.html`
- `GatsToGo Staff Terminal.dc.html`
- `GatsToGo Design System.dc.html`

## Notes And Caveats

- Paystack and SMS need live-credential testing in your own environment before going to production -- see Current Status above.
- The Docker migration service runs `*.up.sql` files from `migrations` (idempotently -- see `migrations/migrate.sh`).
- The Postgres and Redis ports exposed in `docker-compose.yaml` (`5435`, `6379`) are for local access only; remove or firewall them for any real deployment.
- Per-token identity on the staff terminal (e.g. "token #42") is not tracked as its own record -- the number shown is the running `tokens_issued` counter for the shift, an aggregate, not a per-token row. A dispensed weight (empty/filled cylinder weight) is also not captured -- there's no scale integration; filled tickets show "awaiting fill" for net weight until one exists.
- An owner-initiated cash movement (no shift context of its own) is attributed to the plant's most recently opened shift; a shift-picker UI for that case doesn't exist yet.
