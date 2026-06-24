# syntax=docker/dockerfile:1

# ---- build stage ----
FROM golang:1.22-alpine AS build
WORKDIR /src

# Cache deps first.
COPY go.mod go.sum ./
RUN go mod download

# Build the static binary.
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/fanout ./cmd/fanout

# ---- runtime stage ----
FROM alpine:3.24
# tzdata is required for Provider C origin-timezone math (SPEC §5.5); ca-certs
# for completeness. Run as a non-root user.
RUN apk add --no-cache tzdata ca-certificates && \
    adduser -D -u 10001 fanout
USER fanout

COPY --from=build /out/fanout /usr/local/bin/fanout

EXPOSE 8090
ENV PORT=8090
ENTRYPOINT ["/usr/local/bin/fanout"]
