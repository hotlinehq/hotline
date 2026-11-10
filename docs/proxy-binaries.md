# The proxy as a program file

Agreed with Peter on 2026-11-10. Files: `.github/workflows/binary-build.yml`, `binary-pr.yml`, `binary-release.yml` and `.github/scripts/smoke-program.sh`.

## What it is

Besides the Docker image, each release offers the proxy as one program file, called `hotline-proxy`, like the Docker image, for Linux and macOS, on Intel (amd64) and ARM (arm64). The files are on GitHub Releases of this repository. Download one, check it, run it, with no Docker.

## Decisions (Peter, 2026-11-10)

1. **Four programs**, built on a Linux machine by Go's own cross build, static, with the Dockerfile's flags (`CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.buildVersion=<version>"`).
2. **An SBOM** (software bill of materials, the list of libraries inside the programs) in CycloneDX form, `hotline-proxy_<version>_sbom.cdx.json`, written by `cyclonedx-gomod`, pinned by version, and covered by `SHA256SUMS` and its signature (Peter, 2026-11-10). One file serves all four programs, since they have the same libraries. Its build notes name linux/amd64 and `CGO_ENABLED=0`, the settings of the Linux amd64 program; the other three differ only in system and architecture.
3. **File names:** `hotline-proxy_<version>_<os>_<arch>.tar.gz`, holding `hotline-proxy` and `LICENSE`. One `SHA256SUMS` lists all four. `<version>` is the git tag, or `edge` for main.
4. **When:** as the image, with the same `src/hotline-proxy/**` filter. A tag `v1.2.3` makes the release `v1.2.3`. A push to `main` replaces the one pre-release called `edge`, so its download addresses never change and nothing piles up.
5. **Build once, check, then publish.** `build` makes each of the four programs in its own job, all running at the same time (Peter, 2026-11-10), and `package` gathers them with the SBOM and `SHA256SUMS`. `smoke-linux` runs the amd64 program on every build, checking `SHA256SUMS`, the version and `validate` of the sidecar example's config. The Mac programs are only built, never run: there is no Mac runner and none will be bought (Peter, 2026-11-10), so the Linux run is the only check. Only then `binary-release.yml` publishes the same files. Pull requests that change the proxy run the build and checks and publish nothing.
6. **Signed checksums.** `SHA256SUMS` is signed with cosign keyless (tied to the workflow, no key to keep); the signature is `SHA256SUMS.sigstore.json`. A separate build provenance action was not added: it could not be pinned from the session that wrote this.
7. **No GoReleaser.** A short workflow does it.
8. **The program prints its own name, version and commit:** `hotline-proxy v1.2.3 (commit 0123456789ab)`; the edge build prints `hotline-proxy <commit>`. The build passes both in; a local `go build` in a git checkout uses the commit Go records.
9. **Mac programs are not signed or notarized.** macOS shows an unidentified developer warning on a downloaded file. Clear it once with `xattr -d com.apple.quarantine hotline-proxy`, or build with `go build ./cmd` in `src/hotline-proxy`.

## The Docker image

The same workflows make the image (Peter, 2026-11-10). The Dockerfile compiles nothing: it copies the Linux programs from these same files onto the distroless base, the check compares the image's program with the download byte for byte, and publish pushes the image and the release files together, only when every check passed. The old `image-build.yml`, `image-pr.yml` and `image-release.yml` are gone; `make image` builds the image locally. Detail: [proxy-docker-image.md](proxy-docker-image.md).

## Check a download

```
sha256sum -c SHA256SUMS --ignore-missing     # on a Mac: shasum -a 256 -c SHA256SUMS --ignore-missing
cosign verify-blob SHA256SUMS --bundle SHA256SUMS.sigstore.json \
  --certificate-identity-regexp '^https://github.com/petercipov/hotline/' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```

## Not here

A Homebrew tap, Windows, signing and notarizing, `.deb` and `.rpm` packages, and the macOS firewall setup for the intercept ingress.
