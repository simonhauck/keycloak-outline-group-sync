# syntax=docker/dockerfile:1
# Base images are digest-pinned (multi-arch manifest lists) for reproducible builds.

FROM golang:1.26-alpine@sha256:ce864e7223ac17b1775e6fd0b4c0db580c2eb50e7953a427916379e4b92a1628 AS build
WORKDIR /src
COPY go.mod ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" \
    -o /out/keycloak-outline-group-sync ./cmd/keycloak-outline-group-sync

FROM alpine:3.23@sha256:fd791d74b68913cbb027c6546007b3f0d3bc45125f797758156952bc2d6daf40
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
RUN addgroup -S -g 10001 app \
    && adduser -S -D -H -u 10001 -G app app
COPY --from=build /out/keycloak-outline-group-sync /usr/local/bin/keycloak-outline-group-sync
USER 10001:10001
ENTRYPOINT ["/usr/local/bin/keycloak-outline-group-sync"]
