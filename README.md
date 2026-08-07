# GatsToGo

GatsToGo is a multi-tenant LPG plant web app. Each gas plant gets a white-labelled customer site, while GatsToGo provides internal owner, admin, and staff terminal surfaces for operating the plant.

The current app is a Go server with server-rendered `templ` pages, PostgreSQL-backed tenant data, and lightweight client-side interactions for demo UI flows.

## What Is Here

- Customer site for buying gas without signup or login
- Receipt page focused on proof of gas received
- Staff terminal for plant attendants, cashiers, operators, and combined staff
- Owner dashboard for plant operations
- Admin console for platform operations
- PostgreSQL schema for tenants, users, prices, shifts, tickets, cash movements, reserved slugs, and activity logs
- Brand foundation prototypes in `gatstogo-brand-foundation/project`

## Tech Stack

- Go `1.26`
- Chi router
- `templ` for server-rendered HTML
- PostgreSQL via `pgx`
- Docker Compose for local app/database setup
- Air for hot reload in the development container
- HTMX is loaded on the older base layout
- Alpine is used on the customer buy form
- Vanilla JavaScript is used for the owner/admin tabs and staff terminal demo behavior
- Static CSS files are served from `web/static/css`

## Project Structure

```text
cmd/server/main.go                 Go server, routes, DB loaders
internal/domain                    Database model structs
internal/middleware                Tenant lookup middleware
migrations                         Database schema and seed SQL
web/templates                      templ source files
web/templates/**/*_templ.go        generated templ files
web/static/css                     page CSS
gatstogo-brand-foundation/project  design prototypes
```

Do not edit generated `*_templ.go` files by hand. Edit the `.templ` files, then regenerate.

## Pages

### Public Tenant Pages

These pages require a plant subdomain-style host because the tenant middleware reads the plant slug from the first host segment.

Example:

```text
http://sunrise.localhost:8080/
```

Routes:

- `/` - customer buy gas home page
- `/home` - same customer home page
- `/receipts` - customer receipts and receipt proof UI
- `/terminal` - staff terminal UI
- `/owner` - owner dashboard
- `/owner/dashboard` - same owner dashboard
- `/home/summary` - small plant summary component endpoint

### Platform Page

This page is outside the tenant middleware:

- `/admin` - GatsToGo admin console

## Current UI Status

The customer and staff terminal flows are UI-first/demo flows right now. They use local state and placeholders where API work is not implemented yet.

The owner and admin pages are connected to the current database schema and show placeholders/empty states when records are missing.

## Environment Variables

The server reads:

```env
APP_PORT=8080
APP_DATABASE_URL=postgres://user:password@host:5432/gatstogo?sslmode=disable
ADMIN_DATABASE_URL=postgres://admin_user:password@host:5432/gatstogo?sslmode=disable
```

`APP_DATABASE_URL` is intended for normal tenant traffic. `ADMIN_DATABASE_URL` is intended for platform-level reads and operations.

## Run With Docker

From the project root:

```bash
docker compose up --build
```

The app listens on:

```text
http://localhost:8080
```

For tenant pages, use a plant subdomain host. The seed data creates a `sunrise` plant, so use:

```text
http://sunrise.localhost:8080/
http://sunrise.localhost:8080/terminal
http://sunrise.localhost:8080/owner
```

Admin console:

```text
http://localhost:8080/admin
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

1. Start PostgreSQL.

2. Create a database:

```bash
createdb gatstogo
```

3. Apply the schema and seed data:

```bash
export DATABASE_URL="postgres://gatstogo:gatstogo_secret@localhost:5432/gatstogo?sslmode=disable"
psql "$DATABASE_URL" -f migrations/0001_init_schema.up.sql
psql "$DATABASE_URL" -f migrations/0002_seed_data.up.sql
```

4. Create a `.env` file:

```env
APP_PORT=8080
APP_DATABASE_URL=postgres://gatstogo:gatstogo_secret@localhost:5432/gatstogo?sslmode=disable
ADMIN_DATABASE_URL=postgres://gatstogo:gatstogo_secret@localhost:5432/gatstogo?sslmode=disable
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

Run the compile/test check:

```bash
GOCACHE=/private/tmp/gatstogo-go-cache go test ./...
```

If you are not on macOS, you can use the normal Go cache:

```bash
go test ./...
```

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
- `reserved_slugs` - protected subdomains
- `users` - owners, managers, cashiers, operators, admins
- `prices` - immutable price history
- `shifts` - shift open/close records and drawer state
- `tickets` - customer purchases, payment state, fill state, receipt proof
- `cash_movements` - cash drawer movements
- `activity_log` - audit trail

Row-level security policies are included in the schema for tenant isolation. The schema also defines an app role and an admin role for production-style separation.

## Tenant Resolution

Tenant pages use the first part of the host as the plant slug:

```text
sunrise.localhost:8080 -> sunrise
```

Reserved/system slugs such as `www`, `admin`, and `api` are rejected by the tenant middleware.

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

There is also a pitch deck at:

```text
/Users/sinclair/Documents/GatsToGo/documents/gatstogo-pitch.pdf
```

## Notes And Caveats

- Customer purchase, receipt verification, payment, SMS, and terminal actions are not fully wired to APIs yet.
- `/terminal` is currently an interactive UI prototype with local state.
- Owner/admin pages read from the database and use empty states when data is missing.
- The Docker migration service runs `*.up.sql` files from `migrations`.
- The app currently loads some third-party scripts/fonts from CDNs. CSS has already been moved to local static files.
