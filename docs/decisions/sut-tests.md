# Tests shaped around a SUT struct

SUT means system under test: the part a test drives and checks. Here it is also a small struct that owns all setup, so each test reads in a few lines. How to write one: the [`sut-tests` skill](../../.claude/skills/sut-tests/SKILL.md).

- **Every test uses a SUT struct** (Peter, 2026-11-28). Reason: tests should read like short stories. Setup, defaults, error checks and waiting live in one place, so a change to the code changes one sut method, not every test. Reference commits, Peter's own: `a046178` (defaults moved out of the call sites), `cd3e60c` (one sut per `Context`), `77ddc68` (the godog test split into one sut per area), and `git show 668dfa9:src/hotline/servicelevels/pipelne_test.go`.
- **The rework comes in two steps** (Peter, 2026-11-28). First the Ginkgo tests in `src/hotline-proxy`, which today use none. Then the godog `world` struct (`world_test.go`, about 85 fields shared by 39 step files) is split into one sut per area. Where today's tests drift, as found on 2026-11-28:
  - `internal/forward/forwarding_test.go`: each test builds its own request and reaches into the `rig`'s mutex and fields.
  - `internal/export/disk_test.go`: an `open` closure instead of a sut, files changed by hand, and `export.QueueFileSize`, exported only for tests.
  - `internal/ledger/ledger_test.go`: `BeforeEach` builds a signer and a record with internal calls.
  - `world_test.go`: one struct for every area, instead of one sut per area as in `77ddc68`.
- **Remove a test-only bridge in the same step as the rework** (Peter, 2026-11-28, Q3). Revisit if: removing a bridge makes a rework too large to review on its own.
- **A broad public constructor instead of a bridge** (Peter, 2026-11-28). A part that a solitary test builds gets one constructor, named for what it builds and used by production too, and the test passes its fakes through it: `export.NewQueue(export.QueueSetup{...})` replaced `export.NewTestQueue`. Tests stay in the `<pkg>_test` package. This makes the part visible to the other packages that import it; Peter accepted that.

## The rewrite of every proxy test (2026-11-28)

- **Q1: the disk queue's files get their own package, `internal/queuefiles`** (Peter, 2026-11-28). The export queue uses it, its tests use it directly, and `QueueSetup.Disk` lets `export.NewQueue` build a disk-backed queue, in place of `disk_internal_test.go`. Reason: the files have a format and edge cases of their own (a cut-off tail, skipped files) that `export.New` cannot set up at the right moment. Revisit if: the package's API grows methods that only tests call.
- **Q2: a behaviour no public way in can reach keeps a small in-package test, which exports nothing** (Peter, 2026-11-28), like `pathprobe/poll_internal_test.go`. This covers three tests: the sync failure of the queue files, the file writer's failed rotation, and a spans list whose length does not fit. Reason: no test is dropped, and nothing is exported for tests or hooked into production. Revisit if: more than a handful of tests need it, which would mean the public ways in are too narrow.
- **Q3: the rest of the rewrite plan as written** (Peter, 2026-11-28): the one-time event moves to `internal/onetime` (`onetime.NewEvent`), `export.ErrBusy` is exported for a sender to say the collector is busy, `export.NewQueue` returns an error, and the godog `world` splits into `proxySUT`, `appSUT`, `vendorsSUT` and one sut per area, each handing its config to the root with `addConfig`. Plan: `sut-tests/rewrite-plan.md` in the project files.

## Tripwires (2026-11-28)

- Confirmation: the `sut-tests` skill still names the type `<area>SUT` (the `sut-tests` scenario in `decisions.feature`). Once the Ginkgo rework is done, this check grows to every `Describe` holding a `sut`.
- Revisit if the Ginkgo rework shows the pattern does not make the tests easier to read, before the `world` split starts.
