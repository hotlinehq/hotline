# The proxy as a program file

Agreed with Peter on 2026-11-10. Files: `.github/workflows/binary-build.yml`, `binary-pr.yml`, `binary-release.yml` and `.github/scripts/smoke-program.sh`.

## What it is

Besides the Docker image, each release offers the proxy as one program file, called `hotline-proxy`, like the Docker image, for Linux on Intel (amd64) and ARM (arm64), and for macOS on Apple silicon (arm64). There is no macOS Intel build (Peter, 2026-11-10): Intel Macs are obsolete. The files are on GitHub Releases of this repository. Download one, check it, run it, with no Docker.

## Decisions (Peter, 2026-11-10)

1. **Three programs**, built on a Linux machine by Go's own cross build, static, with the Dockerfile's flags (`CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.buildVersion=<version>"`).
2. **An SBOM** (software bill of materials, the list of libraries inside the programs) in CycloneDX form, `hotline-proxy_<version>_sbom.cdx.json`, written by `cyclonedx-gomod`, pinned by version, and covered by `SHA256SUMS` and its signature (Peter, 2026-11-10). One file serves all three programs, since they have the same libraries. Its build notes name linux/amd64 and `CGO_ENABLED=0`, the settings of the Linux amd64 program; the other two differ only in system and architecture.
3. **File names:** `hotline-proxy_<version>_<os>_<arch>.tar.gz`, holding `hotline-proxy` and `LICENSE`. One `SHA256SUMS` lists all three. `<version>` is the git tag, or `edge` for main.
4. **When:** as the image, with the same `src/hotline-proxy/**` filter. A tag `v1.2.3` makes the release `v1.2.3`. A push to `main` replaces the one pre-release called `edge`, so its download addresses never change and nothing piles up.
5. **Build once, check, then publish.** `build` makes each of the three programs in its own job, all running at the same time (Peter, 2026-11-10), and `package` gathers them with the SBOM and `SHA256SUMS`. `smoke-linux` runs the amd64 program on every build, checking `SHA256SUMS`, the version and `validate` of the sidecar example's config. The Mac program is not run in CI: a separate Mac smoke job made the release wait for a Mac runner, so it was removed (Peter, 2026-11-11). It is checked by hand on a Mac. Only then `binary-release.yml` publishes the same files. Pull requests that change the proxy run the build and checks and publish nothing.
6. **Signed checksums.** `SHA256SUMS` is signed with cosign keyless (tied to the workflow, no key to keep); the signature is `SHA256SUMS.sigstore.json`. A separate build provenance action was not added: it could not be pinned from the session that wrote this.
7. **No GoReleaser.** A short workflow does it.
8. **The program prints its own name, version and commit:** `hotline-proxy v1.2.3 (commit 0123456789ab)`; the edge build prints `hotline-proxy <commit>`. The build passes both in; a local `go build` in a git checkout uses the commit Go records.
9. **The Mac program is signed and notarized** (Peter, 2026-11-11). Signing stamps it with the Developer ID certificate (your name, from Apple); notarizing means Apple scanned it and approved it. Both are done by `.github/scripts/sign-mac-program.sh` on a macOS runner with Apple's own `codesign` and `notarytool`, in the job `sign-mac` of `binary-build.yml`. Every build of main (so `edge`) and every release tag is signed; pull requests are not, and never see the secrets. The checksums and the cosign signature are made after signing. A bare program cannot carry Apple's approval note, so the very first run on a Mac needs internet. The `xattr` step is no longer needed. Requirements and the one-time Apple setup: `macos-ingress/signing-requirements.md` in the project files, and [mac-signing-setup.md](mac-signing-setup.md).

## The Docker image

The same workflows make the image (Peter, 2026-11-10). The Dockerfile compiles nothing: it copies the Linux programs from these same files onto the distroless base, the check compares the image's program with the download byte for byte, and publish pushes the image and the release files together, only when every check passed. The old `image-build.yml`, `image-pr.yml` and `image-release.yml` are gone; `make image` builds the image locally. Detail: [proxy-docker-image.md](proxy-docker-image.md).

## Running it by hand

On the Actions page, "binary release" has a Run workflow button (Peter, 2026-11-10). Pick `main` to publish `edge` again, or a release tag to publish that release again. On any other branch it builds and checks but publishes nothing.

## Homebrew

After edge is published, the job `tap` in `binary-release.yml` rewrites the edge recipe in the tap `hotlinehq/homebrew-hotline` with the new commit and the checksum of the Mac program, in one commit (Peter, 2026-11-11). Peter installs it with `brew tap hotlinehq/hotline` and `brew install hotline-proxy-edge`. It runs only on main, never for release tags. It is a job of its own: if it fails, edge stays published and only that job is red. It needs the repository secret `HOMEBREW_TAP_TOKEN`, a fine-grained token that can write to the tap repository only. The recipe text lives in the tap's `scripts/update-edge.sh`. The edge tag is made by the pipeline when it publishes, so run the workflow from main with Run workflow rather than creating the tag by hand. Requirements: `homebrew-edge/requirements.md` in the project files.

## Check a download

```
sha256sum -c SHA256SUMS --ignore-missing     # on a Mac: shasum -a 256 -c SHA256SUMS --ignore-missing
cosign verify-blob SHA256SUMS --bundle SHA256SUMS.sigstore.json \
  --certificate-identity-regexp '^https://github.com/petercipov/hotline/' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```

## Not here

Numbered releases in the tap, Windows, a Mac Intel build, `.pkg` or `.dmg` files for the Mac program, `.deb` and `.rpm` packages, and the macOS firewall setup for the intercept ingress.
