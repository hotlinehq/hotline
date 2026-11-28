# Tests shaped around a SUT struct

SUT means system under test: the part a test drives and checks. Here it is also a small struct that owns all setup, so each test reads in a few lines. How to write one: the [`sut-tests` skill](../../.claude/skills/sut-tests/SKILL.md).

- **Every test uses a SUT struct** (Peter, 2026-11-28). Reason: tests should read like short stories. Setup, defaults, error checks and waiting live in one place, so a change to the code changes one sut method, not every test. Reference commits, Peter's own: `a046178` (defaults moved out of the call sites), `cd3e60c` (one sut per `Context`), `77ddc68` (the godog test split into one sut per area), and `git show 668dfa9:src/hotline/servicelevels/pipelne_test.go`.
- **The rework comes in two steps** (Peter, 2026-11-28). First the Ginkgo tests in `src/hotline-proxy`, which today use none. Then the godog `world` struct (`world_test.go`, about 85 fields shared by 39 step files) is split into one sut per area. Where today's tests drift, as found on 2026-11-28:
  - `internal/forward/forwarding_test.go`: each test builds its own request and reaches into the `rig`'s mutex and fields.
  - `internal/export/disk_test.go`: an `open` closure instead of a sut, files changed by hand, and `export.QueueFileSize`, exported only for tests.
  - `internal/ledger/ledger_test.go`: `BeforeEach` builds a signer and a record with internal calls.
  - `world_test.go`: one struct for every area, instead of one sut per area as in `77ddc68`.

## Tripwires (2026-11-28)

- Confirmation: every `Describe` in a new or changed Ginkgo test file holds a `sut`, except fuzz tests, benchmarks and one-function tables (see the skill).
- Revisit if the Ginkgo rework shows the pattern does not make the tests easier to read, before the `world` split starts.
