# Hotline docs

## Running the proxy

- [The hotline-proxy program](proxy-program.md): commands, flags, stopping, exit codes.
- [Config reload without a restart](proxy-config-reload.md)
- [Health checks on the admin port](proxy-health-checks.md)
- [The Docker image](proxy-docker-image.md), with a Kubernetes sidecar example.
- [The proxy as a program file](proxy-binaries.md), for Linux and macOS without Docker.
- [TLS from apps to the proxy](proxy-tls-from-apps.md)

## Ingresses: how apps reach the proxy

- [Forward proxy, the first ingress](proxy-slice-1.md)
- [CONNECT, with interception](proxy-connect.md)
- [The vendor URL in the path](proxy-url-in-path.md)
- [The Host header, with HTTP/2](proxy-host-header.md)
- [Transparent redirect](proxy-transparent-redirect.md)
- [Guarding against calls to internal addresses](proxy-internal-guard.md)

## Checking calls against the vendor's spec

- [Checks against the OpenAPI spec](proxy-spec-checks.md)
- [Hotline's marks in a vendor spec](spec-marks.md): every `x-hotline-` mark, what it's for, with an example.
- [Overlays on a spec](proxy-overlays.md)
- [XML bodies and S3](proxy-xml-bodies.md)
- [AWS through the proxy](proxy-aws.md)
- [AWS's query form: routes picked by a body field](proxy-aws-query.md)

## What the proxy sends

- [Records and their fields over OpenTelemetry](otel-export.md)
- [Each call's timings as a trace](proxy-traces.md)
- [Signed records](proxy-signatures.md)
- [Body fields and headers in the record](proxy-body-fields.md)
- [Trace context on records](proxy-trace-context.md)
- [OTLP over HTTP](proxy-otlp-protocols.md)
- [Writing every call to a local file](proxy-file-export.md), with bodies and every header when asked
- [Printing every call to the console](proxy-console-export.md), coloured, like mitmdump
- [The proxy's own telemetry](proxy-telemetry.md)

## Examples

- [The shop: four apps on AWS](proxy-shop-example.md)
- [An app sending to AWS SQS](proxy-sqs-example.md)

## Measurements

- [t-digest under the radix tree](tdigest.md)
