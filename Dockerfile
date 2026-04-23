# ── Stage 1: Build ────────────────────────────────────────────────────────────
FROM golang:1.22-alpine AS builder

WORKDIR /app

# Download modules first (cached unless go.mod / go.sum change)
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# Build scraper binary
# Note: the promoter (promote-staging.ts) is a TypeScript script that lives in
# the consumer application — it is NOT part of this Go service.
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-w -s" -o /bin/scraper ./cmd/scraper

# ── Stage 2: Scraper runtime image ────────────────────────────────────────────
FROM gcr.io/distroless/static-debian12 AS scraper

COPY --from=builder /bin/scraper /scraper

# Default env vars (override at runtime)
ENV LOG_LEVEL=info \
    SCRAPER_INTERVAL="@every 6h" \
    RUN_ONCE=false \
    SCRAPER_VERSION=1.0

ENTRYPOINT ["/scraper"]
