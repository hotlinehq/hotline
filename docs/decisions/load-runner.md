# Load test runner

Decided by Peter, 2026-11-09.

- The `load test` workflow runs on a computer of our own, a free Oracle Cloud ARM machine with 2 cores, through a GitHub runner (the program that takes jobs from the Actions tab) labelled `hotline-load`. GitHub's shared machines made the numbers jump with other people's jobs.
- The proxy runs on core 0, k6 with the fake vendor and receiver on core 1 (`LOAD_PROXY_CPUS` and `LOAD_OTHER_CPUS` in the workflow).
- The machine is ARM, so its numbers are not comparable with runs on GitHub's Intel machines (runs 25 to 27). Compare builds only on the same machine, best with `compare_with`.
- The repository is public. The workflow must stay `workflow_dispatch` only; never add `pull_request` or `pull_request_target` to a workflow that uses this runner. Settings: approval required for all outside collaborators, and the label is used by this workflow only.
- Oracle may take back a machine whose processor, network and memory use all stay under 20% for 7 days. If the machine disappears, build a new one with the same steps.
- First measurement on the new machine: run the workflow with `compare_with: main` on a copy of `main`. The spread it shows is the noise of this machine.
