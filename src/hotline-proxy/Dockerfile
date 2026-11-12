# syntax=docker/dockerfile:1
#
# The hotline-proxy image: only the program. Vendor specs come with the
# config, next to it in /etc/hotline/config/. The image compiles nothing: it
# copies the finished Linux programs, so it holds the very program that is
# released as a download (Peter, 2026-11-10). Make the programs first, then
# build it from the root of the repo:
#
#   make image
#
# The programs go in dist/image/linux_<arch>/hotline-proxy. In CI the release
# workflow unpacks them there from the downloadable files.
# Decisions (Peter, 2026-11-01): docs/proxy-docker-image.md.

# Google's distroless static image: CA bundle, time zone data and the
# nonroot user, with no shell and no package manager.
FROM gcr.io/distroless/static-debian13:nonroot@sha256:25ac8f0c3b2334a9eb05452266bd4697864a8c8ef0ef71e5e6480d7f92868329
ARG TARGETARCH
COPY --chmod=755 dist/image/linux_${TARGETARCH}/hotline-proxy /hotline-proxy
USER 65532:65532
EXPOSE 8080 8081
ENTRYPOINT ["/hotline-proxy"]
CMD ["run", "--format", "json", "--config", "/etc/hotline/config/hotline-proxy.yaml"]
