# Mock provider — multi-stage build producing a small static binary.
#
# BUILD CONTEXT: the repository ROOT (the directory containing both
# `mockprovider/` and `fixtures/`). This lets the image BAKE the frozen
# fixtures at /fixtures so the binary runs with no mounts.
#
#   docker build -f mockprovider/Dockerfile -t mini-firefly/mockprovider .
#
# Fixtures can be overridden at runtime with FIXTURES_DIR (e.g. a mounted path).

# ---- build stage ----
FROM golang:1.22-alpine AS build
WORKDIR /src

# Module files first for layer caching. (No external deps; go.sum may be absent.)
COPY mockprovider/go.mod ./
RUN go mod download

# Source.
COPY mockprovider/ ./

# Static build, trimmed.
ENV CGO_ENABLED=0 GOOS=linux
RUN go build -trimpath -ldflags="-s -w" -o /out/mockprovider ./cmd/mockprovider

# ---- runtime stage ----
# alpine (not scratch) so the binary has tzdata for real timezone math (Provider
# C local-time conversions, Provider B UTC epochs).
FROM alpine:3.20
RUN apk add --no-cache tzdata ca-certificates && adduser -D -u 10001 app
WORKDIR /app

COPY --from=build /out/mockprovider /usr/local/bin/mockprovider
# Bake the frozen fixtures into the image at the default FIXTURES_DIR.
COPY fixtures/ /fixtures/

USER app
ENV PORT=8080 FIXTURES_DIR=/fixtures
EXPOSE 8080

ENTRYPOINT ["/usr/local/bin/mockprovider"]
