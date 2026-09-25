# ── 1. Build the Tailwind CSS ──────────────────────────────────────────────
FROM node:20-alpine AS css
WORKDIR /src
COPY package.json ./
RUN npm install --no-audit --no-fund
COPY tailwind.config.js ./
COPY web ./web
COPY internal/server/templates.go ./internal/server/
RUN npm run build:css

# ── 2. Build the Go binary ─────────────────────────────────────────────────
FROM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=css /src/web/static/app.css web/static/app.css
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/ ./cmd/...

# ── 3. Small runtime image (runs as a non-root user) ───────────────────────
FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata && adduser -D -u 10001 app
COPY --from=build /out/ /app/
USER app
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=5s --start-period=20s CMD wget -qO- http://127.0.0.1:8080/healthz >/dev/null || exit 1
ENTRYPOINT ["/app/server"]
