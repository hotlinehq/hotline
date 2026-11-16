# Load test runner

Decided by Peter, 2026-11-09.

- The `load test` workflow runs on a computer of our own, a free Oracle Cloud ARM machine with 2 cores, through a GitHub runner (the program that takes jobs from the Actions tab) labelled `hotline-load`. GitHub's shared machines made the numbers jump with other people's jobs.
- The proxy runs on core 0, k6 with the fake vendor and receiver on core 1 (`LOAD_PROXY_CPUS` and `LOAD_OTHER_CPUS` in the workflow).
- The machine is ARM, so its numbers are not comparable with runs on GitHub's Intel machines (runs 25 to 27). Compare builds only on the same machine, best with `compare_with`.
- The workflow runs after every merge to `main`, and by hand (Peter, 2026-11-16). Only one load test runs at a time and none is cancelled: the single `hotline-load` runner takes one job at a time and the rest wait in GitHub's queue, for up to 24 hours. Do not add a concurrency group, because GitHub keeps only one waiting run per group and cancels the older one. Do not add a second runner with this label, or two tests would run at once.
- The repository is public. The workflow may start only on a push to `main` or by `workflow_dispatch`; never add `pull_request` or `pull_request_target` to a workflow that uses this runner. Settings: approval required for all outside collaborators, and the label is used by this workflow only.
- Oracle may take back a machine whose processor, network and memory use all stay under 20% for 7 days. If the machine disappears, build a new one with the same steps.
- First measurement on the new machine: run the workflow with `compare_with: main` on a copy of `main`. The spread it shows is the noise of this machine.
