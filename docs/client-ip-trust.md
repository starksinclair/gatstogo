# Known issue: client IP is taken from a spoofable header

**Status:** open, not fixed. **Severity:** high, because it defeats brute-force protection on owner, manager and admin password logins.
**Found:** code review before launch. **Code:** [`clientIP`](../cmd/server/accounts.go) in `cmd/server/accounts.go`.

## What the code does

```go
func clientIP(r *http.Request) string {
	if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
		parts := strings.Split(fwd, ",")
		return strings.TrimSpace(parts[0])      // leftmost entry
	}
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	return host
}
```

It returns the **leftmost** `X-Forwarded-For` entry. The leftmost entry is the one the *caller* wrote. A proxy appends the address it saw to the end of whatever the client already sent, so the first entry can be anything the visitor chooses. The code comment says the header is "set by a trusted reverse proxy", but nothing checks that the request came from one, and on a direct connection (local dev, or any host with no proxy) the visitor controls the header completely.

## Demonstrated

On the local stack (no proxy), against the bank-lookup endpoint whose limit is 20 requests per 5 minutes per IP:

```
same header value x23:        200-class x20, then 429 429 429      <- limiter works
different header every time:  400 400 400 400 400                  <- limiter bypassed
```

Rotating `X-Forwarded-For: 198.51.100.N` on each request is enough to never be limited.

## What is affected

Every limiter keyed on `clientIP(r)`:

| Endpoint | Limiter key | What the bypass allows |
|---|---|---|
| Owner/manager login (`auth.go`) | `owner_login_rl:` | **Unlimited password guessing.** There is no per-account lockout, only this per-IP limit. |
| Platform admin login (`auth.go`) | `admin_login_rl:` | **Unlimited password guessing against admin accounts.** Same reason. |
| Forgot password (`auth.go`) | `forgot_password_rl:` | Unlimited reset emails (email bombing, Resend cost). |
| Signup `/get-started` (`marketing.go`) | `signup_rl:` | Unlimited Paystack payout-account creation. |
| Bank lookup `/accounts/resolve` (`accounts.go`) | `accounts_resolve_rl:` | Unlimited account-name probing. |
| Login finder (`support_pages.go`) | `login_finder_rl:` | Unlimited plant/phone probing. |
| Help feedback (`support_pages.go`) | `help_feedback_rl:` | Unlimited feedback spam. |

Not affected: the staff PIN limiter and the receipts OTP limiter, which are keyed per user or per phone rather than per IP.

**Second effect, lockout of other people.** Because the key is attacker-chosen, someone can send requests with a *victim's* address in the header and use up that address's budget, so the real person is shown "Too many attempts". For example an office sharing one public IP can be locked out of owner login.

## Related trust assumptions

- `requestScheme` (`tickets.go`) trusts `X-Forwarded-Proto` the same way. Lower impact, since it only chooses `http` vs `https` when building the Paystack callback URL, but it should follow the same rule.
- `r.Host` is the tenant boundary and is already validated against the configured base domain (`middleware/tenant.go`), so it is not part of this issue. It does matter for the platform choice, see [cloudflare-deployment.md](cloudflare-deployment.md).

## Correct behaviour depends on where the app runs

| Deployment | Where the real client address lives | Notes |
|---|---|---|
| Local dev, no proxy | `r.RemoteAddr` | Ignore all forwarding headers. |
| Behind Cloudflare (any origin, including Cloudflare Containers) | `CF-Connecting-IP` | Cloudflare sets it and a visitor cannot forge it, **but only if the origin is reachable solely through Cloudflare** (Tunnel, Authenticated Origin Pulls, firewall to Cloudflare ranges, or a Worker that adds a secret header). Otherwise anyone hitting the origin directly can forge it too. |
| Google Cloud Run / Google load balancer | Entry appended by Google, counted from the **right** | Google appends the client address to any `X-Forwarded-For` the client sent. Take the entry at the trusted position from the right, never the first. Verify the exact position with a test request before relying on it. |
| Another reverse proxy (nginx, Caddy, Fly, a VPS) | Entry appended by that proxy, from the right | Same rule: count from the right by the number of proxies you trust. |

## Recommended fix

1. Add a config setting, for example `TRUSTED_PROXY`, with these modes:
   - `none` (default): use `r.RemoteAddr` only and ignore `X-Forwarded-For`, `X-Forwarded-Proto` and `CF-Connecting-IP`.
   - `cloudflare`: use `CF-Connecting-IP`.
   - `xff:N`: take the Nth entry from the right of `X-Forwarded-For`.
2. Parse the result with `net.ParseIP`. If it is missing or invalid, fall back to `r.RemoteAddr`. Never use a raw header string as a Redis key.
3. For IPv6, bucket by `/64` so one visitor cannot get unlimited addresses.
4. Apply the same mode to `requestScheme` and `X-Forwarded-Proto`.
5. Defence in depth, independent of IP: add a **per-account** lockout for owner, manager and admin password logins (for example 10 failures per phone number per 15 minutes), the way staff PINs already work. An IP limit alone is not enough protection for a password login.

## Tests to write

- Mode `none`: a spoofed `X-Forwarded-For` or `CF-Connecting-IP` is ignored.
- Mode `cloudflare`: `CF-Connecting-IP` is used, and an invalid value falls back to `RemoteAddr`.
- Mode `xff:N`: client-prepended entries are ignored, the Nth from the right is used.
- IPv6 addresses in one `/64` share one bucket.
- Regression: the "rotate the header 5 times" demonstration above must end in `429`.
