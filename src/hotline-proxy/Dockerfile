# syntax=docker/dockerfile:1
#
# The hotline-proxy image: only the program. Vendor specs come with the
# config, next to it in /etc/hotline/config/. Build it from the root of the repo:
#
#   docker buildx build -f src/hotline-proxy/Dockerfile --build-arg VERSION=v0.1.0 .
#
# Decisions (Peter, 2026-11-01): docs/proxy-docker-image.md.

# The build runs on the build machine's own platform and Go cross-compiles
# for the target one, so a multi-arch build needs no emulation.
FROM --platform=$BUILDPLATFORM golang:1.27.3-trixie@sha256:af1bfc2614ebb1dbd31af514b78740fd678850e76498762b425ecee4c5c6ea80 AS build
ARG TARGETOS
ARG TARGETARCH
# VERSION is the release's git tag, printed by hotline-proxy version.
ARG VERSION
WORKDIR /src
COPY src/hotline-proxy/go.mod src/hotline-proxy/go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY src/hotline-proxy/ ./
# One static program with Go's own name lookup: no C library needed.
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH GOWORK=off \
    go build -trimpath -ldflags "-s -w -X main.buildVersion=${VERSION}" -o /out/hotline-proxy ./cmd

# Google's distroless static image: CA bundle, time zone data and the
# nonroot user, with no shell and no package manager.
FROM gcr.io/distroless/static-debian13:nonroot@sha256:25ac8f0c3b2334a9eb05452266bd4697864a8c8ef0ef71e5e6480d7f92868329
COPY --from=build /out/hotline-proxy /hotline-proxy
USER 65532:65532
EXPOSE 8080 8081
ENTRYPOINT ["/hotline-proxy"]
CMD ["run", "--config", "/etc/hotline/config/hotline-proxy.yaml"]
