# The proxy's Docker image

Agreed with Peter on 2026-11-01. Files: `src/hotline-proxy/Dockerfile`, `.github/workflows/image-build.yml`, `image-pr.yml`, `image-release.yml`, `.github/dependabot.yml` and the sidecar example in `deploy/kubernetes/sidecar/`. Test: the version scenario in `src/hotline-proxy/features/program.feature`, and the smoke test in the image workflow.

## What it is

One hardened image, `ghcr.io/petercipov/hotline-proxy`, for `linux/amd64` and `linux/arm64`. It holds the `hotline-proxy` program, built as one static file, and nothing else of ours. It runs as a non-root user on a read-only filesystem, with no shell and no package manager. Each published image carries an SBOM (software bill of materials, the list of what is inside), build provenance (a signed note of where and how it was built) and a cosign signature.

## Decisions (Peter, 2026-11-01)

1. **Base image:** Google's distroless static image, `gcr.io/distroless/static-debian13:nonroot`, pinned by digest. Distroless means only the files a program needs: the CA bundle (the trusted issuers for vendor certificates), time zone data, a few files in `/etc` and the `nonroot` user (65532). Chainguard's static image, Alpine (it has a shell and a package manager) and an empty scratch image were looked at and not taken. Dependabot proposes a new digest each week, so Debian's security fixes reach the image.
2. **Static build with Go's own name lookup** (`CGO_ENABLED=0`). Go's resolver reads the same `/etc/resolv.conf`, `/etc/hosts` and `/etc/nsswitch.conf` as Linux, so Kubernetes search domains and `hostAliases` work, and `vendorTimeout` can cut a slow lookup short. Linux's own resolver, from the C library, would only add lookup plugins that containers don't have, and would need a C library in the image and a C compiler for each platform.
3. **Multi-arch:** `linux/amd64` and `linux/arm64` under one name. Go cross-compiles both on the build machine, with no emulation.
4. **No vendor specs in the image** (Peter, 2026-11-02; they were in their own layer from 2026-11-01). Specs come with the config, in the same ConfigMap at `/etc/hotline/config/`, and the config points to them by a relative path.
5. **Memory:** the proxy does not read the container's memory limit; like Envoy, HAProxy and Squid, it takes a fixed setting. Set `GOMEMLIMIT` (Go's soft memory cap) a little under the container's limit, for example `230MiB` for `256Mi`. Without it Go does not know the limit, can grow past it under load, and Kubernetes kills the proxy mid-call.
6. **No health check command.** The image has no shell or curl, so no Docker `HEALTHCHECK`. Kubernetes probes `/livez` and `/readyz` on the admin port: [proxy-health-checks.md](proxy-health-checks.md). A `health` command comes only if a real user needs it, for example with Docker Compose.
7. **Releases are git tags.** Pushing `v1.2.3` to the `hotline` repo publishes `1.2.3`, `1.2` and `latest`. Every push to `main` publishes `edge`. Every published build is also `sha-<short commit>`. `hotline-proxy version` prints the git tag (or the commit for `main`), passed in at build time. The first release is `v0.1.0`.
8. **SBOM, provenance and signature on every published image.** The signature is cosign keyless: it is tied to the GitHub workflow that built the image, so there is no key to keep. Check it with:
   ```
   cosign verify ghcr.io/petercipov/hotline-proxy:0.1.0 \
     --certificate-identity-regexp '^https://github.com/petercipov/hotline/' \
     --certificate-oidc-issuer https://token.actions.githubusercontent.com
   ```

## Inside the image

- `/hotline-proxy`, the program; the image starts it with `run --config /etc/hotline/config/hotline-proxy.yaml`.
- Ports `8080` (proxy) and `8081` (admin) are only the examples' choice; the config's `listen` section decides.
- The program writes nothing to disk, handles SIGTERM, SIGINT and SIGHUP itself and starts no other program, so it runs as process 1 with no helper.

## Where files go

| Path | From | Holds |
|---|---|---|
| `/etc/hotline/config/` | ConfigMap | the config, its vendor specs and overlays, in one ConfigMap so they change in one step |
| `/etc/hotline/keys/` | Secret | `fingerprintKey` files |
| `/etc/hotline/tls/` | Secret, often from cert-manager | the `listen.tls` certificate and key |
| `/etc/hotline/connect/` | Secret | the `listen.connect` issuer |

Each is its own folder, side by side: Kubernetes and Docker can't mount a folder inside a read-only mounted folder. Changed files are picked up within 5 seconds with no restart ([proxy-config-reload.md](proxy-config-reload.md)); keys read from `env` change only on a restart.

## Running it

With Docker, to try it out:

```
docker run --rm --read-only --stop-timeout 30 -p 8080:8080 -p 8081:8081 \
  -v "$PWD/config:/etc/hotline/config:ro" -v "$PWD/keys:/etc/hotline/keys:ro" \
  -e GOMEMLIMIT=230MiB --memory 256m \
  ghcr.io/petercipov/hotline-proxy:0.1.0
```

`--stop-timeout 30` gives the proxy its full 25 second stop; Docker's default of 10 seconds cuts it short.

On Kubernetes as a sidecar: `deploy/kubernetes/sidecar/` is a kustomize example. Make the key Secret, then apply:

```
kubectl create secret generic hotline-proxy-keys --from-file=fingerprint.key=./fingerprint.key
kubectl apply -k deploy/kubernetes/sidecar
```

The proxy is a native sidecar (an init container with `restartPolicy: Always`), so it starts before the app and stops after it. The app reaches it on `localhost:8080`, with its integration ID as the user name in the proxy address. The admin port is never in a Service.

To debug, `kubectl debug -it <pod> --image=busybox --target=hotline-proxy` attaches a container with a shell to the running pod; the proxy image stays without one.

## Building it

From the root of the repo:

```
docker buildx build -f src/hotline-proxy/Dockerfile --build-arg VERSION=v0.1.0 \
  --platform linux/amd64,linux/arm64 -t hotline-proxy .
```

Pull requests run `image-pr.yml`, but only when they change `src/hotline-proxy/` (the Dockerfile is in it), so a change to the load test, docs or any workflow (the image workflows too) builds no image (Peter, 2026-11-02); main (again only for those changes) and release tags (always) run `image-release.yml`. Both use the same `build` and `smoke` jobs from `image-build.yml`. `build` builds both platforms once, with the SBOM and provenance, and saves the image as a file. `smoke` loads the `linux/amd64` image from that file and checks it (version, `validate` and `run` of the sidecar example's config on a read-only filesystem, `/readyz`, the user). Then `image-release.yml` alone runs `publish`, which copies the same file to ghcr.io with `skopeo`, which comes with the GitHub runner, and signs it, so the published image is the one that was checked. Pull requests run `build` and `smoke` and push nothing.

## Cleaning up old images

Every main push adds an image named `edge` and `sha-<commit>`, so old main builds pile up. `image-cleanup.yml` runs every Monday and by hand. It never touches releases (`1.0.1`, `1.0`, `latest`, any `v*` name) or `edge`, keeps the 5 newest `sha-` builds, and deletes older ones together with their platform images, SBOM, provenance and cosign signature (Peter, 2026-11-02).

Each build is several entries in the registry: only the top one carries a name, and the platform images and attached records show as "untagged". A plain "delete untagged" tool would delete parts of live images, so the job uses `dataaxiom/ghcr-cleanup-action`, which deletes those parts only with their image.

For now every run only lists what it would delete. Once a listed run looks right, the scheduled run is switched to deleting. The log prints each deleted version's id, and GitHub can restore a deleted version for 30 days.

## Later

A shared proxy example (a Deployment behind a Service, with `listen.tls` or `listen.connect`), and a Helm chart.
