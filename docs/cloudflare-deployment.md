# Running GatsToGo on Cloudflare: what is possible

**Status:** research, no deployment done yet. Prices and limits below were read from Cloudflare's docs in November 2026 (links at the bottom). Cloudflare changes prices, so re-check before committing money.

## Summary

- **Cloudflare Containers can run the existing Go Docker image.** It went generally available on 13 April 2026. No rewrite is needed, only a small JavaScript/TypeScript Worker in front of it.
- **It is not cheaper than Cloud Run at pilot size.** The Workers Paid plan has a $5/month minimum, and an always-on small container costs roughly $7 to $13/month in total. Cloud Run's scale-to-zero free tier is about $0 for a quiet pilot (from memory of Google's pricing, not re-checked).
- **The real reasons to use Cloudflare** are the free wildcard HTTPS for `<slug>.gatstogo.com`, the global edge in front of the app, R2 object storage with no download fees, and a path to plant custom domains. These do not depend on where the Go server runs.
- **Postgres and Redis cannot move to Cloudflare.** The app depends on Postgres row-level security (Cloudflare's own database, D1, is SQLite) and on Redis commands (INCR/EXPIRE for sessions, OTPs and rate limits). Neon and Upstash stay.
- **One thing must be tested before committing:** whether a container can open raw TCP connections to Neon (port 5432) and Upstash. The docs imply yes but never say so outright. See "Spike".

## What the app needs

| Need | Today | Notes |
|---|---|---|
| Run a Go server | Docker image (`Dockerfile`, final stage), listens on `APP_PORT` | Stateless; sessions are in Redis |
| Per-plant subdomains | `<slug>.gatstogo.com`, tenant read from `r.Host` | Wildcard DNS and HTTPS required |
| Postgres with row-level security | Neon, pooled endpoint | Core security design, cannot change engine |
| Redis | Upstash | Sessions, PIN limiter, OTP, rate limits |
| File storage | Local disk (`web/static/uploads`), wiped on redeploy in a container | Only plant logos, 52 KB today |
| Inbound webhooks | Paystack `POST /webhooks/paystack` | Needs a stable public URL |
| Outbound calls | Paystack, Resend, SMS provider | Plain HTTPS |
| Health check | `GET /healthz` (added) | Checks both databases and Redis |

## Component by component

| Component | Cloudflare option | Verdict |
|---|---|---|
| DNS, HTTPS, CDN, DDoS | Free plan, proxied wildcard record `*.gatstogo.com` | **Yes.** Universal SSL covers the root domain and first-level subdomains for free. `sunrise.gatstogo.com` is first-level so it is covered; `a.b.gatstogo.com` would not be. |
| Go server | **Cloudflare Containers** (Worker + Docker image) | **Yes, with a small Worker.** Details below. |
| Static files (`/static/*`) | Workers static assets (free, unlimited requests) or R2 | **Yes, optional.** Keeps CSS and images off the container. |
| Logos and uploads | **R2** (S3-compatible API) | **Yes.** About $0 at this size. Needs a small code change in `internal/uploads`. |
| Plant custom domains (the "Domain" field) | **Cloudflare for SaaS** | **Yes, later.** 100 hostnames free, then $0.10 each, up to 50,000. |
| Postgres | none | **No.** D1 is SQLite and cannot replace row-level-security Postgres. Keep Neon. Cloudflare can resell PlanetScale Postgres but that is a different, probably dearer, database. |
| Redis | none | **No.** Keep Upstash. Replacing it with Workers KV or Durable Objects means rewriting the session, OTP and limiter code. |
| Real client IP | `CF-Connecting-IP` header | **Yes, and needed.** Fixes [the client IP issue](client-ip-trust.md). |

## Cloudflare Containers: the facts

**How it works.** A Worker receives the request, picks a Durable Object (one per container), starts the container if needed, and forwards the request to a port inside it. Container instances start on demand. Users cannot connect to the container directly, only through the Worker, and only with HTTP.

**Plan.** Requires Workers Paid, $5/month, which includes some usage.

**Billing.**

| Resource | Included per month | Overage |
|---|---|---|
| Memory | 25 GiB-hours | $0.0000025 per GiB-second |
| CPU | 375 vCPU-minutes | $0.000020 per vCPU-second |
| Disk | 200 GB-hours | $0.00000007 per GB-second |
| Network egress (North America and Europe) | 1 TB | $0.025 per GB |

Billed per 10 ms while the container is running. **Memory and disk are billed on what the instance type provisions, for as long as it is awake. CPU is billed only on active use.** It stops billing when the instance sleeps. Workers requests and each container's Durable Object are billed in addition (Workers Paid includes 10 million requests per month, then $0.30 per million). Container logs bill like Workers Logs.

**Instance types.**

| Type | vCPU | Memory | Disk |
|---|---|---|---|
| lite | 1/16 | 256 MiB | 2 GB |
| basic | 1/4 | 1 GiB | 4 GB |
| standard-1 | 1/2 | 4 GiB | 8 GB |
| standard-2 | 1 | 6 GiB | 12 GB |
| standard-3 | 2 | 8 GiB | 16 GB |
| standard-4 | 4 | 12 GiB | 20 GB |

**Lifecycle.**
- Cold start from fully stopped is "often in the 1-3 second range".
- The default idle timeout (`sleepAfter`) is 10 minutes, after which the container stops.
- **Disk is ephemeral.** A restart gives a fresh disk from the image. (Fine for this stateless app once uploads go to R2.)
- On deploy or shutdown the container gets `SIGTERM`, up to 15 minutes to exit, then `SIGKILL`. The Go server already handles `SIGTERM` gracefully.
- The docs mention built-in autoscaling only as a future item, so assume you choose how many instances exist.

**Networking.** Internet access is on by default. Ports other than 80 and 443 are not intercepted by Cloudflare's outbound handlers and are not blocked while internet access is enabled, which implies raw TCP to a database works. The docs do not state it explicitly.

## What it would cost

Computed from the rates above, including the $5 plan minimum, one instance running all month (30 days):

| Setup | Memory | Disk | CPU | Plan | **Total / month** |
|---|---|---|---|---|---|
| `lite`, always on, CPU busy 10% | $1.40 | $0.31 | $0.00 | $5 | **about $6.70** |
| `basic`, always on, CPU busy 10% | $6.26 | $0.68 | $0.85 | $5 | **about $12.80** |
| `basic`, always on, CPU busy 30% | $6.26 | $0.68 | $3.44 | $5 | **about $15.40** |
| `basic` x2 for redundancy | $12.74 | $1.40 | $2.14 | $5 | **about $21.30** |
| `basic`, awake about 8 h/day | $2.04 | $0.20 | $0.00 | $5 | **about $7.25** |
| `standard-1`, always on | $25.70 | $1.40 | $2.14 | $5 | **about $34.20** |

Not included: Durable Object charges (not researched, expected small), Workers requests beyond 10 million, Neon, Upstash, Resend, SMS, and Paystack fees.

**Comparison.** At pilot scale Cloud Run with scale-to-zero should cost about $0 (re-check Google's current free tier). Cloudflare becomes competitive when you want always-on capacity: a Cloud Run instance kept warm costs more than the `basic` rows above, and you also avoid the Google load balancer (about $18/month) that Cloud Run needs for wildcard domains. `lite` has only 1/16 of a vCPU and 256 MiB; a Go server probably fits in memory but test it under load before trusting it.

## Wildcard domains and plant domains

- **`<slug>.gatstogo.com`:** one proxied wildcard DNS record. Universal SSL covers it at no cost.
- **Why Cloud Run needs extra work (confirmed in Google's docs):** Cloud Run's built-in domain mapping is in *preview*, states "You cannot use wildcard certificates with this feature", and says it is "not recommended for production services". The region we planned for (`europe-west2`) is not in its supported list. Google's recommendation for production is a global external Application Load Balancer (paid). So do not plan around domain mapping at all.
- **Cloudflare in front of Cloud Run:** Cloudflare can serve `*.gatstogo.com` over HTTPS to browsers, but Cloud Run only routes requests addressed to its own `*.run.app` hostname. Rewriting the `Host` header with an Origin Rule is **Enterprise only** (Free, Pro and Business cannot), so a **Worker** is needed to call the `run.app` URL. The app then no longer sees the plant's hostname in `r.Host`, so the Worker must pass it in a header, and the app must trust that header only when the request carries a shared secret (the `run.app` URL is public, so anyone can call it directly). See "Cloud Run + Cloudflare: what it takes" below.
- **With Containers:** the Worker receives the original host and forwards the request, so the tenant check should keep working unchanged, and the container has no public URL to bypass the Worker. Confirm in the spike.
- **Plants' own domains (for example `gas.theirbusiness.com`):** Cloudflare for SaaS custom hostnames. 100 free, then $0.10 each, up to 50,000 on Free/Pro/Business. Wildcard custom hostnames are Enterprise only, which does not matter here.

## R2 for uploads

- $0.015 per GB-month. The free tier includes 10 GB-month, 1 million write operations and 10 million read operations per month. Downloads to the internet are free.
- At this size, cost is zero. The benefit is that files survive redeploys.
- Implementation: replace the disk write in `internal/uploads` (`SaveLogo`) with an S3-API upload to R2, store the public URL in `plants.logo_path`, and serve it from a bucket custom domain such as `assets.gatstogo.com`. This works regardless of where the Go server runs.

## Changes needed in this repo

1. **Fix the client IP trust** ([client-ip-trust.md](client-ip-trust.md)): add a `TRUSTED_PROXY=cloudflare` mode that reads `CF-Connecting-IP`, and make sure the container is only reachable through the Worker.
2. **Honour `X-Forwarded-Proto`** only when the proxy is trusted (the Worker connection may arrive as plain HTTP inside Cloudflare).
3. **Graceful shutdown** is already in place: `cmd/server/main.go` handles `SIGINT`/`SIGTERM` and calls `srv.Shutdown`. Confirm its timeout is shorter than the platform's grace period.
4. **Logos to R2** (above).
5. **A Worker project** (`wrangler.jsonc` plus a short `index.ts`): routes `*.gatstogo.com/*` and `gatstogo.com/*` to the container, sets the container's default port, idle timeout and instance count, and waits for `/healthz` before forwarding.
6. **Secrets:** move the values in `.env.prod.example` into Worker secrets and variables.
7. **Update** the Paystack webhook URL and `APP_BASE_URL` once the domain points at Cloudflare.

## Spike: the questions to answer before committing

Build a throwaway Worker and container from the existing production image on a Workers Paid account, then check:

| Question | Pass condition |
|---|---|
| Does a container reach Neon on port 5432 and Upstash over TCP/TLS? | `/healthz` returns 200 from inside Cloudflare. |
| Does the app still see `Host: sunrise.gatstogo.com`? | The tenant page loads for a plant subdomain and a bogus subdomain 404s. |
| Cold start | Measure from Nigeria after the container has slept. Acceptable if the first page load is under a few seconds. |
| Warm latency to Neon | A typical customer page renders in a time you are happy with from Nigeria. Compare Neon regions. |
| Memory fit | `lite` or `basic` survives a simple load test without restarts. |
| Deploy behaviour | A redeploy does not drop in-flight requests. |
| Paystack webhook | Signature verification passes through the Worker. |
| Real client IP | `CF-Connecting-IP` reaches the app and cannot be forged from outside. |

## Cloud Run + Cloudflare: what it takes

Confirmed facts: Cloud Run domain mapping is not usable (above); Origin Rule Host-header and SNI overrides are Enterprise only; Transform Rules and Origin Rules exist on the Free plan but cannot rewrite the Host; Cloudflare's docs recommend `CF-Connecting-IP` over `X-Forwarded-For` (which Cloudflare appends to rather than replaces) but do not state that a client-supplied `CF-Connecting-IP` is overwritten, so do not rely on that header alone.

Design:

1. **Worker** on routes `*.gatstogo.com/*` and `gatstogo.com/*`. For each request it calls the `run.app` URL and adds three headers: the original host (`X-Original-Host`), the visitor address taken from `CF-Connecting-IP` (`X-Client-IP`), and a long random shared secret (`X-Origin-Secret`).
2. **Go middleware** (runs first): if `X-Origin-Secret` matches the configured secret (constant-time compare), use `X-Original-Host` as the request host and `X-Client-IP` as the client IP. If it does not match, ignore those headers entirely, so a direct hit on the `run.app` URL resolves to no plant and no marketing site (404) and cannot forge an IP.
3. `clientIP`, `requestScheme`, `middleware.Tenant` and the marketing-host checks read the effective host and IP from that middleware instead of `r.Host` and the raw headers. This is the same change described in [client-ip-trust.md](client-ip-trust.md), with the secret added.
4. Cloud Run stays `--allow-unauthenticated` (the Worker does not use Google IAM). Point the Paystack webhook at `https://gatstogo.com/webhooks/paystack` so it goes through the Worker.
5. **Worker limits:** the Free Workers plan allows 100,000 requests per day, and every page, stylesheet and image request goes through the Worker. That covers roughly 10,000 page views a day at 10 requests each; the $5 plan removes the cap. Serving `/static` from Workers static assets or R2 cuts the count.

Compared with the other options: Containers has no public URL to bypass, and a VPS can be firewalled to Cloudflare's address ranges or use Authenticated Origin Pulls, and passes `Host` through unchanged, so neither needs the shared-secret scheme. Cloud Run + Cloudflare works but is the most glue of the three.

## Decision options

| Option | Cost at pilot | Effort | Notes |
|---|---|---|---|
| A. Cloud Run + Cloudflare in front | about $0 | Medium: trusted-host-header code change plus client IP fix | Cheapest while quiet. Keeps GCP. |
| B. Cloudflare Containers | about $7 to $13 | Medium: Worker, client IP fix, R2 | One platform for DNS, edge, compute and storage. No load balancer. Needs the spike. |
| C. One small VPS running `docker-compose.yaml`, Cloudflare in front | about $5 | Medium: you run updates, backups, monitoring | Cheapest always-on. Host header passes through unchanged. |

Option B is the best fit if you want everything on Cloudflare. Option A is the best fit if you want the lowest bill until real traffic arrives.

## Sources

- [Containers pricing](https://developers.cloudflare.com/containers/platform/pricing)
- [Containers limits](https://developers.cloudflare.com/containers/platform-details/limits/)
- [Containers overview](https://developers.cloudflare.com/containers/)
- [Container lifecycle and architecture](https://developers.cloudflare.com/containers/platform-details/architecture/)
- [Handle outbound traffic](https://developers.cloudflare.com/containers/platform-details/outbound-traffic/)
- [Containers and Sandboxes generally available (13 April 2026)](https://developers.cloudflare.com/changelog/post/2026-04-13-containers-sandbox-ga/)
- [Workers pricing](https://developers.cloudflare.com/workers/platform/pricing/)
- [R2 pricing](https://developers.cloudflare.com/r2/pricing/)
- [Hyperdrive pricing](https://developers.cloudflare.com/hyperdrive/platform/pricing/)
- [Cloudflare for SaaS plans](https://developers.cloudflare.com/cloudflare-for-platforms/cloudflare-for-saas/plans/)
- [Universal SSL limitations](https://developers.cloudflare.com/ssl/edge-certificates/universal-ssl/limitations/)
