# Shop example on Floci (Peter, 2026-11-12)

The files are in `examples/aws-shop/floci/`; how it works is in its README.

1. The shop example runs against **Floci**, a free local copy of AWS (MIT licence, Docker image `floci/floci`, pinned in `up.sh`). Real AWS is not needed (Peter, 2026-11-12). It was chosen over AWS's own emulator (no prebuilt image yet), MiniStack, LocalEmu and fakecloud: its answers were closest to AWS's in the trial, and per call speed was the same as MiniStack's.
2. **Terraform** makes the resources (OpenTofu works too; whichever is installed). The files are `floci/terraform/main.tf`, reading the policy files in `examples/aws-shop/aws/`.
3. The shop and the proxy are unchanged. Hotline gets `tlsOrigination: false` and `allowInternalIPs: true` per api in `floci/hotline-proxy.yaml`; the AWS host names point to this machine through `/etc/hosts`, and Floci's port 4566 is published on port 80.
4. Every integration has `content: full`, so the file exporter writes every call with headers and bodies to `floci/out/calls/calls.jsonl`. The workflow keeps it as an artifact. The OTLP exporter never gets the bodies.
5. The proxy also sends its records, counts and traces to **Grafana Cloud** when the repository secrets `GRAFANA_OTLP_ENDPOINT` and `GRAFANA_OTLP_AUTHORIZATION` are set.
6. The workflow `aws-shop-floci.yml` starts by hand only, to save quota.
