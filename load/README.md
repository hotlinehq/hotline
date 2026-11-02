# Load test

Measures what the proxy costs a call. Part one of the k6 pipeline: it only measures. Pass or fail rules come later. Plan: the `next-steps` proposals and `load-test/pipeline-requirements.md` in the project files.

## Run it

Install [k6](https://grafana.com/docs/k6/latest/set-up/install-k6/), then `make load`. It builds the proxy, starts a fake vendor and a fake receiver, and for each line of the Makefile runs k6 three times straight to the vendor and three times through the proxy, alternating. Every repeat is kept as it came: nothing is averaged.

Results go to `load/results/run-001`, `run-002` and so on (never overwritten). Each has `repeat-1`, `repeat-2` and `repeat-3` with k6's own summary files and a `result.json`.

One call: `go run ./load/cmd/load -proxy load/out/hotline-proxy -spec specs/aws/ssm/2014-11-06.openapi.yaml -rate 500`.

## What the numbers mean

- **extra p50, p95, p99** are the proxy's delay minus the direct delay, in milliseconds, from k6's `http_req_duration`. p50 is the middle call, p95 and p99 the calls slower than 95 and 99 out of 100. Each repeat runs at a fixed rate (calls keep coming on schedule even when replies slow down).
- **cpu us/call** is the processor time the proxy process used during the run (from `/proc`, or `ps` on a Mac) divided by the calls made, in microseconds. `/proc` counts in hundredths of a second, so very short runs are coarse.
- **allocated KB/call** is the growth of `go.memory.allocated` (memory the proxy handed out, counting memory freed again), from the proxy's own telemetry, from the start of the run to 2 seconds after, divided by the calls made. Go reports it in lumps, so use runs of hundreds of calls a second or more.
- **peak memory** is the highest resident memory of the proxy, sampled once a second during the run.

The fake vendor, receiver and k6 run on the same computer as the proxy, so the numbers compare builds with each other, not with a real deployment.

## Call shapes

`small-json` (SSM `GetParameter`, 200 bytes), `large-json` (SSM `GetParametersByPath`, 115 KB) and `slow` (small, after 50 ms). The fake vendor also has an `xml` shape, but the runner has no S3 call for it yet.
