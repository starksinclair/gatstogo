# ======================
# Stage 1: Builder
# ======================
FROM golang:1.26-alpine AS builder

# Install build dependencies
RUN apk add --no-cache git ca-certificates tzdata

ENV GOBIN=/go/bin
ENV PATH="/go/bin:${PATH}"

WORKDIR /app

# Cache dependencies first
COPY go.mod go.sum ./
RUN go mod download

# Copy the rest of the source code
COPY . .

RUN go install github.com/a-h/templ/cmd/templ@v0.3.1020 && templ generate

# Build the binary
# - CGO_ENABLED=0 → pure static binary
# - -ldflags="-s -w" → smaller binary
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /app/server ./cmd/server

# ======================
# Stage 2: Development image
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
# Stage 3: Final image
# ======================
FROM alpine:3.20

# Install only what we need at runtime
RUN apk add --no-cache ca-certificates tzdata curl

# Create non-root user
RUN addgroup -S app && adduser -S app -G app

WORKDIR /app

# Copy the binary from builder
COPY --from=builder /app/server .

# cmd/server/main.go serves static assets via
# http.FileServer(http.Dir("web/static")) -- a real filesystem read at
# request time, relative to the working directory, not something
# `go build` bakes into the binary the way templ's generated .go files
# are. Without this, every stylesheet, image, and uploaded plant logo
# 404s the moment this image runs anywhere that isn't the dev compose
# setup's own bind mount (docker-compose.yaml's "- .:/app", which only
# exists for the "dev" stage above, not this one).
COPY --from=builder /app/web/static ./web/static

# Writable at runtime: internal/uploads.SaveLogo creates
# web/static/uploads/<slug>/ on demand. A non-root USER (below) can't
# write into a directory it doesn't already own, so this has to be
# created and chowned before switching users -- mkdir -p under the
# now-root `app` user copy, matching where uploads.BaseDir expects it
# relative to WORKDIR.
RUN mkdir -p web/static/uploads && chown -R app:app web/static/uploads

# Use non-root user
USER app

# Expose the port your app listens on
EXPOSE 8080

# Healthcheck
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
  CMD curl -fsS http://localhost:8080/ || exit 1

# Run the binary
CMD ["./server"]