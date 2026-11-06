# Load test

Measures what the proxy costs a call. Part one of the k6 pipeline: it only measures. Pass or fail rules come later. Plan: the `next-steps` proposals and `load-test/pipeline-requirements.md` in the project files.

## Run it

Install [k6](https://grafana.com/docs/k6/latest/set-up/install-k6/), then `make load`. It builds the proxy, starts a fake vendor and a fake receiver, and for each line of the Makefile runs k6 three times straight to the vendor and three times through the proxy, alternating. Every repeat is kept as it came: nothing is averaged.

Results go to `load/results/run-001`, `run-002` and so on (never overwritten). Each has `repeat-1`, `repeat-2` and `repeat-3` with k6's own summary files and a `result.json`.

One call: `go run ./load/cmd/load -proxy load/out/hotline-proxy -spec specs/aws/ssm/2014-11-06.openapi.yaml -rate 500`.

## What the numbers mean

- **extra p50, p95, p99** are the proxy's delay minus the direct delay, in milliseconds, from k6's `http_req_duration`. p50 is the middle call, p95 and p99 the calls slower than 95 and 99 out of 100. Each repeat runs at a fixed rate (calls keep coming on schedule even when replies slow down).
- **cpu us/call** is the processor time the proxy process used during the run (from `/proc`, or `ps` on a Mac) divided by the calls made, in microseconds. `/proc` counts in hundredths of a second, so very short runs are coarse.
- **allocated KB/call** is the growth of `go.memory.allocated` (memory the proxy handed out, counting memory freed again), from the proxy's own telemetry, from 16 seconds before the run to 16 seconds after it, divided by the calls made. The OpenTelemetry Go library re-reads Go's counters at most every 15 seconds, so the test waits 16 seconds before and after each proxy run; a shorter wait read a stale value and showed 90, 35 and 45 KB for the same load. Use runs of hundreds of calls a second or more. The first line printed names the scenario (call, reply size, rate, length, repeats).
- **peak memory** is the highest resident memory of the proxy, sampled once a second during the run.

The fake vendor, receiver and k6 run on the same computer as the proxy, so the numbers compare builds with each other, not with a real deployment.

## Call shapes

`small-json` (SSM `GetParameter`, 200 bytes), `medium-json`, `large-json` and `huge-json` (SSM `GetParametersByPath`, about 30 KB, 115 KB and 500 KB), `slow` (small, after 50 ms) and `xml` (an S3 `ListObjectsV2` of 50 files, about 7 KB of XML). Run `xml` with the S3 spec: `-spec specs/aws/s3/2006-03-01.openapi.yaml`. The others take the SSM spec.

## Judging a fix against main

`make load-compare BASE_PROXY=<the built hotline-proxy of main>` runs one repeat of main and one of this checkout, taking turns, three times over for each of six lines (small 1000 a second, 30 KB at 400, 115 KB at 200 and 500, 500 KB at 50, XML at 500). Nothing is averaged. The table at the end (also in `load/results/compare.txt`) lists every repeat and gives the range of change of the branch against main: from its best repeat to its worst. If even the worst repeat is below main, the fix is real, not noise.

In CI: Actions tab, "load test", Run workflow, pick the branch and fill in `compare_with` (for example `main`).

## Profiles and the exact memory number

`make load` builds the proxy with `-tags loadprofile`, a build for the load test only that serves Go's profiling pages on a local port when the runner asks. The lines marked `-profile` (115 KB at 200 a second, 500 KB at 50, XML at 500) run once more after the repeats, not counted in the numbers, and save `profile/cpu.pprof` (where processor time goes) and `profile/allocs.pprof` (where memory is handed out) in the run folder. Open one with `go tool pprof -http=: load/results/run-005/profile/allocs.pprof`. The proxy that ships is built without the tag and has no profiling page.

The same build also serves `/debug/vars`. When the runner finds that page, it reads the proxy's total bytes handed out from it at once, so it does not wait the 2 times 16 seconds for the telemetry number (about 32 seconds less per repeat). A proxy built without the tag (for example an older `main`) is still measured the old way, with the wait.
