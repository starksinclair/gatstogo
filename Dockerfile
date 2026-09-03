# Production image (Cloud Run / Lightsail / any container host) is the
# final stage so `docker build` and `gcloud run deploy --source` pick it
# by default. Local compose still builds `--target dev`.
#
# Cloud Run:
#   gcloud run deploy gatstogo \
#     --source . \
#     --region europe-west2 \
#     --allow-unauthenticated \
#     --set-env-vars APP_ENV=production,...
# Neon: set APP_DATABASE_URL / ADMIN_DATABASE_URL to Neon URLs with
# sslmode=require (prefer the pooled endpoint for Cloud Run).

# ======================
# Stage 1: Builder
# ======================
FROM golang:1.26-alpine AS builder

RUN apk add --no-cache git ca-certificates tzdata

ENV GOBIN=/go/bin
ENV PATH="/go/bin:${PATH}"

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN go install github.com/a-h/templ/cmd/templ@v0.3.1020 && templ generate

# Static binary for alpine/distroless-style runtimes (Cloud Run).
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o /app/server ./cmd/server
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o /app/gtg ./cmd/gtg

# ======================
# Stage 1b: Migrate CLI (docker compose `migrate` service + Neon ops)
# ======================
FROM alpine:3.20 AS migrate

RUN apk add --no-cache ca-certificates tzdata \
	&& addgroup -S app && adduser -S app -G app

WORKDIR /migrations
COPY --from=builder /app/gtg /gtg
COPY migrations/ /migrations/

USER app
ENTRYPOINT ["/gtg"]
CMD ["migrate", "--seed"]

# ======================
# Stage 2: Development image (docker compose --target dev)
# ======================
FROM golang:1.26-alpine AS dev

RUN apk add --no-cache git ca-certificates tzdata
ENV GOBIN=/go/bin
ENV PATH="/go/bin:${PATH}"
RUN go install github.com/air-verse/air@latest && go install github.com/a-h/templ/cmd/templ@v0.3.1020

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY .air.toml ./

EXPOSE 8080

CMD ["air", "-c", ".air.toml"]

# ======================
# Stage 3: Production / Cloud Run
# ======================
FROM alpine:3.20 AS production

# ca-certificates: Neon (TLS), Paystack, Resend, etc.
# tzdata: correct timestamps in logs / activity
RUN apk add --no-cache ca-certificates tzdata \
	&& addgroup -S app && adduser -S app -G app

WORKDIR /app

COPY --from=builder /app/server .

# Static assets are read from disk at runtime (not embedded).
COPY --from=builder /app/web/static ./web/static

# Uploads dir must be writable by non-root. On Cloud Run this volume is
# ephemeral (lost on new revisions / instances) -- use object storage
# later for durable plant logos.
RUN mkdir -p web/static/uploads && chown -R app:app /app

USER app

# Cloud Run injects PORT (usually 8080). The server reads PORT, then
# APP_PORT, then defaults to 8080 -- see cmd/server/main.go.
ENV APP_ENV=production
ENV PORT=8080

EXPOSE 8080

# Cloud Run ignores HEALTHCHECK; kept for local `docker run` smoke tests.
HEALTHCHECK --interval=30s --timeout=3s --start-period=10s --retries=3 \
	CMD wget -qO- "http://127.0.0.1:${PORT:-8080}/" >/dev/null || exit 1

CMD ["./server"]
