# syntax=docker/dockerfile:1

FROM --platform=$BUILDPLATFORM tonistiigi/xx AS xx

FROM --platform=$BUILDPLATFORM golang:1.24-alpine AS build
COPY --from=xx / /
WORKDIR /app

# Copy module files first for better caching
COPY go.mod go.sum ./
RUN go mod download

# Copy source
COPY . .

# Build (Go handles cross-compilation natively)
ARG TARGETPLATFORM
ARG TARGETOS
ARG TARGETARCH
RUN GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-amd64} go build -trimpath -ldflags="-s -w" -o /uptime-kuma-provisioner .

FROM alpine:3.20
RUN apk add --no-cache ca-certificates
COPY --from=build /uptime-kuma-provisioner /usr/bin/uptime-kuma-provisioner

ENTRYPOINT ["/usr/bin/uptime-kuma-provisioner"]
