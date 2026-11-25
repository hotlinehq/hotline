# Lint decisions

1. **Fix rather than skip.** A lint warning is fixed at its cause. A new skip (a `//nolint` comment, an exclusion rule in `.golangci.yaml`, or a turned-off check) is added only after Peter agrees that no clean fix exists (Peter, 2026-11-24).
2. **Stale skips are reported.** `nolintlint` is on: every skip names its linter and says why, and a skip the linter no longer needs fails lint (Peter, 2026-11-24).
3. **Number conversions** that could in theory not fit go through `internal/fits`, whose functions the compiler inlines (a test checks it). Each caller decides what a misfit means. Conversions that copy bits on purpose keep their skip (Peter, 2026-11-24). Revisit if a checked conversion shows up in a profile.
4. **JSON names** in the proxy are snake case, set once in `.golangci.yaml`; other modules stay camel case (Peter, 2026-11-24).
5. **Files the operator names** (config, specs, certificates, secrets) can live anywhere, as in Envoy and HAProxy, so their reads keep their skip. The file exporter writes only inside its own folder with mode 0640, so the path and mode checks are turned off for `internal/export/file.go` (Peter, 2026-11-24). Revisit if the exporter starts reading paths from outside its folder.
6. **Ginkgo and Gomega** dot imports are accepted everywhere through staticcheck's `dot-import-whitelist` (Peter, 2026-11-24).

## Tripwires (2026-11-25)

- Confirmation: `nolintlint` is in the list of linters `.golangci.yaml` turns on.
- Revisit if: see the Revisit if lines in the decisions above.
