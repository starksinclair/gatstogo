# Database, Neon, and Cloud Run

This project uses **Postgres** (local Docker or Neon) with two app roles, **Redis** for sessions, and SQL migrations applied by the `gtg` CLI (same binary `docker compose` runs).

## Mental model

```text
┌─────────────────────┐     migrate once      ┌──────────────────────────┐
│  gtg migrate         │ ──────────────────►  │  Postgres / Neon         │
│  (owner / superuser) │                      │  schema + roles          │
└─────────────────────┘                      └────────────┬─────────────┘
                                                          │
                    runtime (Cloud Run / docker app)      │
┌─────────────────────┐   APP_DATABASE_URL (RLS)         │
│  gatstogo server     │ ◄────────────────────────────────┤
│                      │   ADMIN_DATABASE_URL (bypass RLS) │
└─────────┬───────────┘                                  │
          │ REDIS_URL                                     │
          ▼                                               │
┌─────────────────────┐                                   │
│  Redis / Upstash     │                                   │
└─────────────────────┘                                   │
```

| Connection | Who | When |
|---|---|---|
| Bootstrap / migrate | Neon **owner** or local `gatstogo` superuser | `gtg migrate` only |
| `APP_DATABASE_URL` | `gatstogo_app` (RLS on) | Every normal plant request |
| `ADMIN_DATABASE_URL` | `gatstogo_admin` (RLS off) | Admin console, plant lookup |

Migrations create those two roles (`0001`) and set their passwords (`0003`) from `GATSTOGO_APP_ROLE_PASSWORD` / `GATSTOGO_ADMIN_ROLE_PASSWORD`. The app must use the **same** passwords in its URLs.

Seed data (`0002_seed_data`) is **opt-in** (demo logins). Local compose always seeds; Neon/production must not, unless you are on a disposable staging branch.

## Teammate: local Docker (no Neon)

```bash
make env          # copies .env.example → .env once
# edit .env: PAYSTACK_SECRET_KEY=sk_test_...  (required for real checkout)
make up           # postgres → gtg migrate --seed → redis → app
```

What `make up` / `docker compose up --build` does:

1. Starts Postgres + Redis  
2. Runs **`gtg migrate --seed`** (schema, role passwords, sunrise demo plant)  
3. Starts the Go app with Air (hot reload)  
4. App waits until migrate **succeeds**

Open:

- Marketing / admin: http://localhost:8080/admin/login  
- Plant site: http://sunrise.localhost:8080/  
- Owner: http://sunrise.localhost:8080/owner/login  

Seeded logins (local only):

| Role | Phone | Password / PIN |
|---|---|---|
| Owner | `08012345678` | `sunrise-owner-dev` |
| Cashier | `08098765432` | `1234` |
| Platform admin | `08000000000` | `platform-admin-dev` |

Optional DB UI: `docker compose --profile tools up adminer` → http://localhost:8083  

Host Postgres port is **5435** (not 5432), so a local `psql` uses `localhost:5435`.

## Neon (hosted Postgres for Cloud Run)

Neon is **not** started by Docker. Local Docker keeps using the compose Postgres. Neon is for staging/production.

### One-time setup

1. Create a Neon project in the **same region** as Cloud Run (e.g. `eu-west-2` / London).  
2. Create database `gatstogo`.  
3. Copy the **direct** connection string (no `-pooler`) for the project owner — that is `MIGRATE_DATABASE_URL`.  
4. Copy the **pooled** (`-pooler`) host for the app later.

### Migrate (from your laptop)

```bash
# In .env.prod (gitignored) or export:
export MIGRATE_DATABASE_URL='postgres://neondb_owner:SECRET@ep-xxxx.eu-west-2.aws.neon.tech/gatstogo?sslmode=require'
export GATSTOGO_APP_ROLE_PASSWORD='strong-app-password'
export GATSTOGO_ADMIN_ROLE_PASSWORD='strong-admin-password'
export APP_ENV=production   # blocks accidental seed

make migrate
# or: go run ./cmd/gtg migrate

make status
```

Do **not** pass `--seed` on production. Create the first real plant via `/admin` after you insert one admin user (seed is intentionally not for prod).

Then set Cloud Run to the **pooled** URLs:

```env
APP_DATABASE_URL=postgres://gatstogo_app:…@ep-xxxx-pooler….neon.tech/gatstogo?sslmode=require
ADMIN_DATABASE_URL=postgres://gatstogo_admin:…@ep-xxxx-pooler….neon.tech/gatstogo?sslmode=require
```

### Why direct vs pooled?

| Use | Host |
|---|---|
| `gtg migrate` | Direct (migrations, `CREATE ROLE`, long sessions) |
| Cloud Run app | Pooled (many short-lived instances) |

## Cloud Run: secrets and variables

The **image build** does not need database credentials. Only the **running service** does. Missing required vars → process exits on startup (fail-fast).

### Required (service will not stay up without these)

Put these in Secret Manager (or `--set-secrets`) where possible:

| Name | Notes |
|---|---|
| `APP_DATABASE_URL` | Neon pooled `gatstogo_app` + `sslmode=require` |
| `ADMIN_DATABASE_URL` | Neon pooled `gatstogo_admin` + `sslmode=require` |
| `REDIS_URL` | Upstash `rediss://…` (TLS) |
| `SESSION_HMAC_SECRET` | `openssl rand -base64 32` (≥16 decoded bytes) |
| `PAYSTACK_SECRET_KEY` | Live or test secret |
| `APP_BASE_DOMAIN` | Apex only, e.g. `gatstogo.com` (not a URL) |
| `APP_BASE_URL` | Public HTTPS origin, no trailing slash |
| `APP_ENV` | `production` (enables Secure cookies) |

Cloud Run also injects `PORT` — do not set `APP_PORT` unless you know you need it.

### Optional (safe to omit; features degrade)

| Name | If missing |
|---|---|
| `RESEND_API_KEY` / `RESEND_FROM_EMAIL` | Password-reset emails only logged |
| `PAYSTACK_PUBLIC_KEY` | Only needed if a client-side Paystack widget uses it |
| `TICKET_NOTIFICATION_EMAIL` | Defaults to built-in address |
| `SMS_PROVIDER` | Defaults to logging (OTP in server logs) |
| `GCS_BUCKET` / `GCS_PUBLIC_BASE_URL` | Not wired yet; logos still use local disk |

### Example deploy sketch

```bash
gcloud run deploy gatstogo \
  --source . \
  --region europe-west2 \
  --allow-unauthenticated \
  --set-env-vars "APP_ENV=production,APP_BASE_DOMAIN=gatstogo.com,APP_BASE_URL=https://YOUR.run.app" \
  --set-secrets "APP_DATABASE_URL=APP_DATABASE_URL:latest,ADMIN_DATABASE_URL=ADMIN_DATABASE_URL:latest,REDIS_URL=REDIS_URL:latest,SESSION_HMAC_SECRET=SESSION_HMAC_SECRET:latest,PAYSTACK_SECRET_KEY=PAYSTACK_SECRET_KEY:latest"
```

Migrate **before** or **right after** first deploy (from CI or laptop with `gtg`), not inside the Cloud Run request path.

## CLI cheat sheet (`gtg`)

```bash
go run ./cmd/gtg migrate           # like ace migration:run
go run ./cmd/gtg migrate --seed    # + demo seed (never on prod)
go run ./cmd/gtg migrate status
go run ./cmd/gtg db:seed
```

Docker compose uses the same binary (Dockerfile `migrate` stage).

`migrations/migrate.sh` remains as a legacy shell runner; prefer `gtg`.

## Common failures

| Symptom | Fix |
|---|---|
| `APP_BASE_DOMAIN is required` | Set in compose `.env` / Cloud Run (compose defaults to `localhost`) |
| `password authentication failed for gatstogo_app` | Role password ≠ URL password; re-run migrate with matching `GATSTOGO_*` |
| Migrate fails on Neon pooler | Use **direct** host in `MIGRATE_DATABASE_URL` |
| App up but buy gas fails | Real `PAYSTACK_SECRET_KEY` (`sk_test_…`) in `.env` |
| Seed refused in production | Expected; use staging or create users via admin |
