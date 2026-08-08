# syntax=docker/dockerfile:1
#
# Logs go to stdout only (cmd/server/main.go:279-287) — there is no file
# appender and no in-process rotation by design. Docker's json-file driver does
# NOT rotate unless you tell it to, so set the limits at run time:
#
#   docker run --log-opt max-size=50m --log-opt max-file=5 ...
#
# or daemon-wide in /etc/docker/daemon.json (applies to new containers only).
# On Kubernetes the kubelet already rotates container logs.

# ---- build ----------------------------------------------------------------
FROM golang:1.26-alpine AS build
WORKDIR /src

# Dependencies first so this layer survives source-only changes.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY . .

# CGO off: pgx, viper and the PDF statement parser are all pure Go, so this
# produces a static binary. The Google Vision OCR provider sits behind the
# `googlevision` build tag and stays out unless you add -tags=googlevision.
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux go build \
      -trimpath -ldflags="-s -w" \
      -o /out/server ./cmd/server

# ---- runtime --------------------------------------------------------------
FROM alpine:3.22

# ca-certificates: outbound TLS to Digitap, Cashfree, Utho, Google, SMTP.
# tzdata: IST-relative timestamps resolve correctly.
RUN apk add --no-cache ca-certificates tzdata \
 && adduser -D -u 10001 app

WORKDIR /app

COPY --from=build /out/server /app/server

# Viper resolves config.yaml relative to the working directory
# (internal/config/config.go:225). Env vars still override every key, and
# APP_PROFILE=<name> merges config.<name>.yaml on top if you mount one.
COPY config.yaml ./config.yaml

# PAN image uploads (registration.pan-image-dir). Mount a volume here in any
# environment where the files must outlive the container.
RUN mkdir -p /app/data/pan-images && chown -R app:app /app/data

USER app
EXPOSE 8080

HEALTHCHECK --interval=30s --timeout=3s --start-period=10s --retries=3 \
  CMD wget -qO- http://127.0.0.1:8080/api/ping >/dev/null 2>&1 || exit 1

# Exec form: the binary is PID 1, so SIGTERM reaches the signal.NotifyContext
# handler in main() and Fiber shuts down gracefully.
ENTRYPOINT ["/app/server"]
