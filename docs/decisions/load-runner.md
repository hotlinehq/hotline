# Load test runner

Decided by Peter, 2026-11-09.

- The `load test` workflow runs on a computer of our own, a free Oracle Cloud ARM machine with 2 cores, through a GitHub runner (the program that takes jobs from the Actions tab) labelled `hotline-load`. GitHub's shared machines made the numbers jump with other people's jobs.
- The proxy runs on core 0, k6 with the fake vendor and receiver on core 1 (`LOAD_PROXY_CPUS` and `LOAD_OTHER_CPUS` in the workflow).
- The machine is ARM, so its numbers are not comparable with runs on GitHub's Intel machines (runs 25 to 27). Compare builds only on the same machine, best with `compare_with`.
- The workflow runs after every merge to `main`, and by hand (Peter, 2026-11-16). Only one load test runs at a time and none is cancelled: the single `hotline-load` runner takes one job at a time and the rest wait in GitHub's queue, for up to 24 hours. Do not add a concurrency group, because GitHub keeps only one waiting run per group and cancels the older one. Do not add a second runner with this label, or two tests would run at once.
- The repository is public. The workflow may start only on a push to `main` or by `workflow_dispatch`; never add `pull_request` or `pull_request_target` to a workflow that uses this runner. Settings: approval required for all outside collaborators, and the label is used by this workflow only.
- Oracle may take back a machine whose processor, network and memory use all stay under 20% for 7 days. If the machine disappears, build a new one with the same steps.
- The noise of this machine is the spread between the three repeats of one build, which every side-by-side run lists for each side. A build compared with itself is refused (below), so this replaces the first measurement that ran a copy of `main` against `main` (2026-11-26).

## Side-by-side runs (Peter, 2026-11-26)

Run 97 was started to judge a fix against `main`, waited two and a half hours in the queue, and by the time it began the fix was merged, so both sides had it. The workflow read the name `main` only when the job started.

- `compare_with` takes a full commit id only (40 characters). A branch or tag name is refused when the job starts, before anything is measured, so the compared commit can't move while the run waits (Q1: option a, a commit id, over a first job on GitHub's machines that would turn a name into one).
- A run whose two sides have the same `src/hotline-proxy` tree stops before measuring: it would show only noise. Both proxies build with the repository's `go.work`, so a dependency bump in another module could change the proxy with its folder unchanged; such a run is refused though the builds differ. That is only ever a wrong refusal, never a wrong measurement.
- `info.txt` and `compare.txt` name both commits.
- Side-by-side runs also measure small calls past what the proxy can handle: `small-json` at 5000, 10000 and 20000 a second (Q2: all three, about 15 more minutes a run). The cost of the stop change (#216) showed most there. Processor time per call is steady there; the slowest 1 in 100 jumps from run to run.

## Tripwires (2026-11-25)

- Confirmation: `load-test.yml` starts only on a push to `main` or by hand, has no concurrency group, and is the only workflow that uses `hotline-load`. It refuses a `compare_with` that is not a commit id and a build compared with itself, and the Makefile's side-by-side lines include `small-json` at 20000 a second.
- Revisit if Oracle takes the machine back more than once, runs wait in the queue for close to 24 hours, or the spread between two runs of the same build hides the changes we want to see. Revisit the commit id rule if Peter often starts these runs himself from the Actions tab and copying an id gets tiresome.
