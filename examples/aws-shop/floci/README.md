# The shop on a local copy of AWS (Floci)

Runs the whole shop story, through Hotline, without an AWS account. [Floci](https://floci.io) is a free local copy of AWS (MIT licence, a Docker image, no sign-up). Terraform makes the shop's resources in it, the same ones [the README](../README.md) lists for real AWS.

```
./run.sh        # start Floci, make the resources, run the story through Hotline, stop everything
```

It needs Docker, Go, and Terraform (or OpenTofu), and the right to change `/etc/hosts` and use port 80 (it uses `sudo` when it is not root). It takes about 45 seconds, 25 of them Terraform's own waiting for queues and the stream.

## How the calls reach Floci

The shop sends each call to Hotline as plain `http` to AWS's real host name, such as `sqs.eu-west-1.amazonaws.com`. [up.sh](up.sh) makes those names point to this machine (lines in `/etc/hosts`, taken out again by [down.sh](down.sh)) and publishes Floci's port 4566 as port 80. [hotline-proxy.yaml](hotline-proxy.yaml) is the proxy's test config with `tlsOrigination: false` (plain http to the vendor) and `allowInternalIPs: true` (this machine is allowed) on every api. The account number comes from the access key, as Floci takes a key of 12 digits as the account.

## What a run leaves in `out/`

- `calls/calls.jsonl`: every call in full, one JSON line each, with all headers and the bodies (see [proxy-file-export.md](../../../docs/proxy-file-export.md)). Read a body with `jq -r '.attributes."hotline.http.request.body" | @base64d' out/calls/calls.jsonl`. Nothing is hidden, so keep it to test data.
- `run-shop.log` and `proxy.log`.

## Grafana Cloud

When `GRAFANA_OTLP_ENDPOINT` and `GRAFANA_OTLP_AUTHORIZATION` are set, the proxy also sends its usual records, counts and traces there (never the bodies: only the file gets those). The endpoint is the OTLP address from the Grafana Cloud portal. The authorization is `Basic ` and the base64 of `<instance id>:<token>`, a token with the write scopes for metrics, logs and traces. They are repository secrets in the workflow, never files.

## The workflow

[.github/workflows/aws-shop-floci.yml](../../../.github/workflows/aws-shop-floci.yml) runs `run.sh` and keeps `out/calls/`, `run-shop.log` and `proxy.log` as the artifact `aws-shop-floci-calls`. It starts only by hand (Actions, then Run workflow).

## Differences from real AWS that Hotline shows

- Floci's SNS and STS replies are not `text/xml`, so Hotline reports `content_type` on their reply. Real AWS does not.
- Floci's CreateTable reply carries zeros that the DynamoDB spec says are at least 1.
- Floci refuses SESv2's tag lookup used by Terraform's newer SES resource, so the sender is made with the older one.
