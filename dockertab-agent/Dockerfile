FROM golang:1.24-alpine AS builder

RUN apk add --no-cache git ca-certificates

WORKDIR /build

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
    -ldflags="-s -w" \
    -o dockertab-agent \
    .

FROM alpine:3.20

RUN apk add --no-cache ca-certificates tzdata

WORKDIR /app
COPY --from=builder /build/dockertab-agent .

# Config directory for persisted secrets and agent identity
RUN mkdir -p /root/.config/dockertab

EXPOSE 8377

HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD wget --no-verbose --tries=1 --spider http://localhost:8377/healthz || exit 1

# Runs as root because Docker socket access requires it.
# Docker socket access is root-equivalent by design.
ENTRYPOINT ["./dockertab-agent"]
