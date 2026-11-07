# Project memory

Decisions already made, so a new session does not have to dig through old threads and pull requests. This file is a short index, read by every session, so keep it small: one line per item here, and the detail in `docs/decisions/` or `docs/`. Add new decisions to the right decisions file, and a line here only for a new topic (Peter, 2026-11-02).

## Where the rules live

- How to test and the TDD loop: [CLAUDE.md](CLAUDE.md).
- How to write, test and review proxy code in Go: the [`hotline-go-proxy` skill](.claude/skills/hotline-go-proxy/SKILL.md). It covers the system under test, testing only through the proxy's ways in and out, and no test-only code.
- Describe each problem in plain words first, with the fix and its tests, and build only after Peter approves (Peter, 2026-11-02).
- Keep to one thread at a time. Do not watch CI or subscribe to pull requests unless Peter asks. When Peter says stop, stop and wait for his go-ahead. If a short "yes" could answer more than one question, ask which one first (Peter, 2026-11-02).
- Every number in a pull request description, commit message or doc is well defined: say what was measured, over what (whole process or proxy only, per call or in total, allocated over time or held at once), how, and its unit (Peter, 2026-11-02).
- Every `x-hotline-` mark in a vendor spec, with its use and an example: [docs/spec-marks.md](docs/spec-marks.md). A new mark is added only when a real example needs it, and listed there in the same pull request (Peter, 2026-11-01).
- The proxy's spec and examples: `proxy-spec.md` and `proxy-example/` in the `hotline-strategic` repo.
- What the proxy sends over OpenTelemetry, field by field: [docs/otel-export.md](docs/otel-export.md). Its own numbers, for the `telemetry` destination: [docs/proxy-telemetry.md](docs/proxy-telemetry.md).

## Code layout

- Examples of apps using Hotline live in `examples/`, each its own Go module listed in `go.work`. The first was `examples/aws-sqs-invoices` (AWS SDK for Go v2, invoice PDF jobs over SQS; [docs/proxy-sqs-example.md](docs/proxy-sqs-example.md), Peter, 2026-10-30), renamed `examples/aws-shop` on 2026-11-01: one program, four apps (`checkout`, `invoices`, `mailer`, `refunds`), each its own integration, and `run-shop.sh` running the whole story. It uses only AWS resources that already exist and never creates or removes any. Detail: [docs/proxy-shop-example.md](docs/proxy-shop-example.md). `run-shop.sh` pays a new order each run (`A-` plus the date and time; any `A-` number is a sample order with A-1001's lines), since an order is invoiced only once, and retries the invoices step while the topic passes the job on (Peter, 2026-11-01).
- The SQS examples and tests use Peter's own AWS account, `192325781350`, in `eu-west-1`, so the example runs against real AWS as is. The recorded signed call in `testdata/aws/sqs-sendmessage-signed.http` keeps `123456789012`, since changing its body would break its signature (Peter, 2026-10-30).
- Stable vendor specs live in `specs/<vendor>/<api>/<vendor version>.openapi.yaml` (for example `specs/aws/sqs/2012-11-05.openapi.yaml`), the vendor's api only. Overlays are test or example specific and live with them, never in `specs/`. Rules: [specs/README.md](specs/README.md) (Peter, 2026-10-30).
- The proxy is its own Go module, `hotline-proxy`, in `src/hotline-proxy/`: the `proxy` package at the top, then `cmd/` (the program), `features/`, `internal/` and `testdata/`. The metrics code stays in module `hotline`, in `src/hotline/metrics/`. Both are listed in `go.work` (Peter, 2026-10-30).

## Testing, in one line each

- Proxy behaviour: Gherkin feature files in `src/hotline-proxy/features/`, run by godog. Everything else: Ginkgo and Gomega.
- The whole proxy is the system under test (the thing a test drives and checks), with fake vendors and a fake OTLP receiver at its edges.
- Never change production code just so a test can reach it.
- Run `make lint` before every push; CI lint finds what tests miss (Peter, 2026-11-02). It needs golangci-lint v2.15 built with the repo's Go: `GOTOOLCHAIN=go1.27.3 go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.15.0` (v2.13 cannot read Go 1.27.3).
- GitHub Actions are pinned to exact commits, with the tag in a comment; Dependabot updates them (Aikido finding, 2026-11-02).

## Slice plan

The proxy is built in small slices, tests first. Each item, with its decisions in full: [docs/decisions/slice-plan.md](docs/decisions/slice-plan.md).

- Built: slice 1 ([docs/proxy-slice-1.md](docs/proxy-slice-1.md)), the program, TLS from apps, health checks, vendor URL in path, internal address guard, Host header ingress, shared vendor apis, spec checks (with cheap loading of large specs), transparent redirect, AWS and SQS, the SQS example, XML bodies and S3, the shop example steps 1 and 3, CONNECT ingress, OTLP protocols, record levels and resource labels, signatures, body fields, overlays, microsecond timings, config reload, proxy telemetry, traces, and the Docker image.
- In progress: the load test with k6, including pooled body buffers and the spec check queue (`specChecks.queueMemory`).
- Still to come: the Node, Java and Rust clients in the end-to-end tests.

## Earlier decisions

- Say "ingress", never "door", for the ways apps reach the proxy (Peter, 2026-10-29).
- The load test uses k6 (Peter, 2026-10-27).

## Decisions by topic

Open only the file for the topic at hand.

- Slice 1: [docs/decisions/slice-1.md](docs/decisions/slice-1.md)
- Program: [docs/decisions/program.md](docs/decisions/program.md)
- TLS from apps: [docs/decisions/tls-from-apps.md](docs/decisions/tls-from-apps.md)
- Vendor URL in path: [docs/decisions/url-in-path.md](docs/decisions/url-in-path.md)
- Internal address guard: [docs/decisions/internal-guard.md](docs/decisions/internal-guard.md)
- Host header ingress: [docs/decisions/host-header.md](docs/decisions/host-header.md)
- Spec checks and shared apis: [docs/decisions/spec-checks.md](docs/decisions/spec-checks.md)
- Transparent redirect: [docs/decisions/transparent-redirect.md](docs/decisions/transparent-redirect.md)
- CONNECT ingress: [docs/decisions/connect.md](docs/decisions/connect.md)
- AWS and SQS: [docs/decisions/aws-sqs.md](docs/decisions/aws-sqs.md)
- SQS example: [docs/decisions/sqs-example.md](docs/decisions/sqs-example.md)
- Health check: [docs/decisions/health-checks.md](docs/decisions/health-checks.md)
- Config reload: [docs/decisions/config-reload.md](docs/decisions/config-reload.md)
- Proxy telemetry: [docs/decisions/proxy-telemetry.md](docs/decisions/proxy-telemetry.md)
- Profile pages on the admin port, `telemetry.pprof`, off by default: [docs/decisions/profile-pages.md](docs/decisions/profile-pages.md)
- Trace context: [docs/decisions/trace-context.md](docs/decisions/trace-context.md)
