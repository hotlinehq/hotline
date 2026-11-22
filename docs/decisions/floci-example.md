# Shop example on Floci (Peter, 2026-11-12)

The files are in `examples/aws-shop/floci/`; how it works is in its README.

1. The shop example runs against **Floci**, a free local copy of AWS (MIT licence, Docker image `floci/floci`, pinned in `up.sh`). Real AWS is not needed (Peter, 2026-11-12). It was chosen over AWS's own emulator (no prebuilt image yet), MiniStack, LocalEmu and fakecloud: its answers were closest to AWS's in the trial, and per call speed was the same as MiniStack's.
2. **Terraform** makes the resources (OpenTofu works too; whichever is installed). The files are `floci/terraform/main.tf`, reading the policy files in `examples/aws-shop/aws/`.
3. The shop and the proxy are unchanged. Hotline gets `tlsOrigination: false` and `allowInternalIPs: true` per api in `floci/hotline-proxy.yaml`; the AWS host names point to this machine through `/etc/hosts`, and Floci's port 4566 is published on port 80.
4. Every integration has `content: full`, so the file exporter writes every call with headers and bodies to `floci/out/calls/calls.jsonl`. The workflow keeps it as an artifact. The OTLP exporter never gets the bodies.
5. The proxy also sends its records, counts and traces to **Grafana Cloud** when `GRAFANA_OTLP_ENDPOINT` and `GRAFANA_OTLP_AUTHORIZATION` are set. The workflow builds them from the same repository secrets and variable as the lint workflow (Peter, 2026-11-22: the probe must show in Grafana Cloud).
6. The workflow `aws-shop-floci.yml` runs after every merge to main, and can still be started by hand (Peter, 2026-11-21; it started by hand only before, to save quota).
7. The shop's `support` app (streamed responses, M2 Q6) runs only here, like the assistant: aimock 1.48.0 plays Anthropic's Messages api at `api.anthropic.com`, behind the same TLS front, so the run needs no Anthropic key. aimock's scripts are matched by the tool each app offers (`orderStatus`, `lookUpOrder`), since both apps ask about the same order. `run.sh` fails when the made-up Anthropic key is in a record or log, or when a stream record was cut or has a broken event (Peter, 2026-11-22, requirement 15).
