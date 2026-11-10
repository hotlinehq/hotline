# Hotline

Hotline keeps track of the third-party APIs your apps call: payments, AWS, booking, anything over HTTP.

The Hotline proxy sits between your apps and those vendors. For every call it:

- checks the request and the reply against the vendor's OpenAPI spec (path, query, headers, cookies, and JSON or XML bodies);
- times each step (DNS lookup, connect, TLS, waiting for the vendor) as an OpenTelemetry trace;
- writes a record of the call, with secrets taken out, signed so it can't be quietly changed later;
- sends it all over OTLP (the OpenTelemetry protocol) to the tools you already use, such as Grafana.

Apps reach the proxy through the ingress that suits them: as an HTTP proxy, with `CONNECT`, with the vendor address in the path, by Host header, or by a transparent redirect. Signed AWS calls stay valid.

## Status

Early, and not released yet. The proxy works end to end and runs against real AWS in the shop example below. Tracking vendor SLAs and alerting on them come next.

## Try it

- [A small shop on AWS](examples/aws-shop/README.md): four apps calling ten AWS services through Hotline, with two mistakes caught on purpose.
- [The Docker image and a Kubernetes sidecar](docs/proxy-docker-image.md).

## Build

You need Go 1.27 or newer. From the root of the repo:

```
cd src/hotline-proxy
go build -o hotline-proxy ./cmd
./hotline-proxy version
```

Or build the Docker image for both platforms, also from the root of the repo. It builds the Linux programs first and the image copies them:

```
make image
```

## Run

1. Write a config file. A full example with comments is in [src/hotline-proxy/testdata/proxy-example/](src/hotline-proxy/testdata/proxy-example/).
2. Check it, then start the proxy:

   ```
   ./hotline-proxy validate --config hotline-proxy.yaml
   ./hotline-proxy run --config hotline-proxy.yaml
   ```

3. Point your apps at it as their HTTP proxy, with the app's name as the user name, for example `HTTPS_PROXY=http://checkout:x@localhost:8080`.
4. Records and traces go to the OTLP addresses in the config, for example a local OpenTelemetry Collector or Grafana Cloud.

Ctrl+C stops it after the calls in progress finish. `./hotline-proxy run --help` lists every flag; details are in [docs/proxy-program.md](docs/proxy-program.md). The proxy picks up changes to its config, specs, keys and certificates within 5 seconds, or at once on `kill -HUP` ([docs/proxy-config-reload.md](docs/proxy-config-reload.md)). Health checks for Kubernetes, `/livez` and `/readyz`, answer on the admin address ([docs/proxy-health-checks.md](docs/proxy-health-checks.md)).

## Test

From the root of the repo:

```
make test    # every module's tests, then the end-to-end tests, with coverage
make e2e     # only the end-to-end tests, against the built program
make lint    # needs golangci-lint
```

For a quick loop on the proxy alone: `cd src/hotline-proxy && go test ./...`. The proxy's behaviour is written as Gherkin scenarios in [src/hotline-proxy/features/](src/hotline-proxy/features/).

## Docs

[docs/README.md](docs/README.md) lists every document, grouped by topic.

## Repo layout

- `src/hotline-proxy`: the proxy.
- `src/hotline`: latency measurements for service level objectives (t-digest and DDSketch windows).
- `src/otel-hotline`: an OpenTelemetry Collector part that uses those measurements; `src/otelcol-dev` is a collector build for trying it.
- `specs/`: vendor OpenAPI specs, such as AWS ([specs/README.md](specs/README.md)).
- `tools/aws-openapi`: makes the AWS specs from AWS's own models.
- `examples/` and `deploy/`: example apps and Kubernetes files.

## License

[Apache 2.0](LICENSE).
