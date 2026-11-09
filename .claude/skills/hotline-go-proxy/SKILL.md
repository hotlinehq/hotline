---
name: hotline-go-proxy
description: How to write Go code for the Hotline proxy - layout, tests first with Gherkin feature files run by godog, standard library HTTP, OpenTelemetry names, errors, concurrency and the proxy's hard rules. Use when designing, writing, testing or reviewing any proxy code in this repo.
---

# Writing the Hotline proxy in Go

The proxy sits between a customer's apps and their vendors. It forwards each call, measures it, checks the bodies against the vendor's OpenAPI spec after the reply has gone back, and sends what it found as OpenTelemetry data.

**The spec is the source of truth:** `proxy-spec.md` in the `petercipov/hotline-strategic` repo. This skill says *how* to code it; the spec says *what* to build. If they disagree, the spec wins; say so and stop rather than guess.

## 1. Tests first, always

This repo works test-first (see `CLAUDE.md`).

### The system under test

The approach follows [Unit testing: values and principles](https://medium.com/codex/unit-testing-values-and-principles-ae80d9210ca6) (Peter, 2026-10-28):

- **The unit is a behaviour, not a function or a struct.** The system under test (the thing a test drives and checks) is a part of the proxy that gives value on its own. For the proxy that is mostly the proxy itself: an app sends a call, the proxy forwards it, a vendor answers, and a record goes out. Functions and types are only the way in, never the subject.
- **Test from the outside in.** A test acts like a user of the proxy: it sends a call the way an app does and looks only at what can be seen from outside: what the app gets back, what the vendor received, what the OTLP receiver got. It knows as little as possible about how the proxy works inside.
- **Fakes only at the edges.** The outside world is replaced at the proxy's boundary: vendors are local servers from `net/http/httptest` (with TLS: `httptest.NewTLSServer`), the OTLP destination is a fake receiver, and the clock and DNS come in through the constructor. Inside the boundary the real parts work together; never stub or mock one part of the proxy to test another. A failure should say what went wrong for the user ("the record kept the customer ID"), not that a mock was not called.
- **No shared constants between code and tests.** Tests write the expected values out in full (`"http.request.method"`, `"REDACTED"`, `"forward_proxy"`), so a wrong constant in the code is caught instead of copied.
- **Small solitary tests only when they pay.** A part with a stable interface and many cases of its own (for example matching a path to a route template) may get its own small Ginkgo test as well. Do this only when driving every case through the whole proxy would be slow or awkward.
- **Tests must pay their cost.** They are deterministic, fast enough that nobody skips them, and free to survive any refactoring that keeps the behaviour. A test that breaks when the inside changes but the behaviour does not is a bad test: rewrite it.
- **Tests are the documentation.** Anyone should be able to read the feature files and know what the proxy does.

### Tests do not mirror the code

From [Unit tests should not mirror source code](https://journal.optivem.com/p/unit-tests-should-not-mirror-source-code-michael-azerhad) (Michaël Azerhad, with Uncle Bob's "TDD harms architecture"; Peter, 2026-10-28):

- **Never one test file per source file, or one test per function.** That one-to-one match ties the tests tightly to the code's structure: every split, merge or move of code breaks tests even when the behaviour stays the same, and the tests end up freezing the design.
- **Organise tests by behaviour.** Feature files and scenarios are named after what the proxy does (`redaction.feature`, "A secret query parameter is always redacted"), never after a package, type or function.
- **A test may cross many types and packages.** Tests are sociable by default: they use the real parts of the proxy together. "Unit" does not mean one function, one struct, or mocking every neighbour.
- **Let the inside emerge.** When refactoring pulls code out into a new function, type or package, it needs no test of its own: the behaviour tests already cover it. Only give it a solitary test when it meets the rule above (stable interface, many cases of its own).
- **Refactoring must not break tests.** If moving logic between types, renaming internals or changing an internal interface breaks a test, the test was coupled to the code: fix the test, not the refactoring.

### Tests do not reach into the code

From [Decoupling tests from implementation details](https://medium.com/codex/decoupling-tests-from-implementation-details-238a9ab06e4f) by Luís Soares (Peter, 2026-10-28): a test says *what* the proxy must do, never *how* it does it.

**Test only through a small set of ways in and out.** For the proxy these are: the config file and specs it reads, the proxy port apps call (and the admin port once it exists), the vendors it calls, and the OpenTelemetry data it sends. Steps use only these to arrange, act and check. They never call internal functions, read internal fields or know which types sit inside.

**Never change production code just to make a test possible.** None of these:
- making a function, field or constant public so a test can use it;
- adding a return value to a function that had none, only to check it; check the effect the outside can see instead;
- adding a function only a test calls;
- a "test mode" flag that changes how the proxy behaves.

The exception is passing in what the proxy needs from the world anyway: the clock, randomness, environment variables, DNS and the vendor transport come in through the constructor (section 3). That makes the code clearer as well as testable.

**No other link between tests and code.**
- Never redo the code's logic in a test ("I'll just recompute it here"). Write the expected value out in full.
- Never use the code's constants or variables in a test. If a test needs a different value, such as a smaller queue, pass it in.
- Don't test helpers on their own, even public ones: a YAML mapper, a header formatter or an OTLP converter exists only to serve a behaviour, so test that behaviour. Keep such helpers private, next to the code that uses them; shared helper packages are a smell.
- Tests don't know about the clock, environment variables or anything else that changes between runs; those are injected.
- Use the same way in to arrange, act and check. To check what was exported, read what the fake OTLP receiver got, not the proxy's queue.

**No snapshot tests.** Don't compare a whole JSON, OTLP payload or log line against a saved copy: any harmless change (field order, a new attribute) turns it red. Check the fields the scenario is about.

**Shape the code so tests see little of it.**
- Everything is private (lower case) until something outside its package truly needs it.
- If part A is the only user of part B, merge them: they change for the same reasons.
- Prefer deep interfaces: a lot of work behind few, simple operations. One call rather than two that must be made in order.
- Each operation does one thing; no modes that switch behaviour on an input.

**Given, When, Then is Arrange, Act, Assert.** Given arranges the starting state, When does one action, Then checks what can be seen from outside.

### Feature files

Behaviour is written as Gherkin feature files (plain-language test cases in Given, When, Then form) in `src/hotline-proxy/features/`, one file per behaviour, e.g. `redaction.feature`. They are run by [godog](https://github.com/cucumber/godog), the official Cucumber tool for Go (MIT license), from `src/hotline-proxy/features_test.go` (Peter, 2026-10-28).

- **Given** sets up the starting state (config, specs, fake vendors), **When** is what an app or vendor does, **Then** is what can be seen from outside. Use `And` to add more lines of the same kind.
- Use `Scenario Outline` with an `Examples` table when the same behaviour has several inputs.
- Use `Background` for setup shared by every scenario in a file.
- Tag open questions from the slice doc (`@q3`) and end-to-end scenarios (`@e2e`). The normal `go test` run skips `@e2e`.
- Step definitions live in Go next to `features_test.go` and drive one system under test object that owns the proxy and its fakes, so every scenario starts clean.

### The loop

1. Before any code, write or update the feature file for the behaviour you are building: the normal cases, the edge cases and the failures. Get it reviewed first.
2. Add step definitions for one scenario so it runs and fails.
3. Run `go test ./...` and see it fail for the right reason.
4. Write the least code that makes it pass, run again, then tidy up. Move to the next scenario.
5. While a feature file has steps with no definition, godog reports them as undefined without failing the run. Once a slice is built, turn the run strict so an undefined step fails.

### Other rules for tests

- godog for behaviour, Ginkgo and Gomega for the small solitary tests. No testify, no mocking libraries.
- Ginkgo test packages are `<pkg>_test` (black box).
- There is no fixed coverage target. Aim for every scenario to run; code that is hard to test is a design smell, so split it.
- Never call the real internet in tests.
- End-to-end tests (Java, Node, Go and Rust clients through every ingress, tagged `@e2e`) are the first milestone's finish line. Keep them separate from the fast tests.

## 2. Dependencies

- **Standard library first.** `net/http`, `net/http/httputil`, `net/http/httptrace`, `crypto/tls`, `crypto/hmac`, `log/slog`, `context`, `sync`.
- Outside modules only where the standard library has nothing: the OpenTelemetry Go SDK and `semconv`, a YAML parser, an OpenAPI and JSON Schema checker. Name every new module and why in the pull request.
- No web frameworks (gin, echo, chi), no dependency-injection frameworks, no helper libraries for things a short loop does.
- The project is open source under Apache-2.0: every new module must have a compatible license (MIT, BSD, Apache-2.0).

## 3. The parts of the proxy

The proxy is a small core with the outside world kept behind interfaces (often called *ports and adapters*: the core only talks to small interfaces, the "ports", and the code that touches real networks, files or clocks, the "adapters", plugs into them). That keeps every part testable with fakes.

**On the call path** (must stay fast; nothing here waits on checks or exports):

1. **Ingress finder:** works out which ingress the call used (forward proxy, Host header, vendor URL in path), reads and removes the integration ID, and picks the integration and api from the current config.
2. **Forwarder:** our own, in `internal/forward`, in place of `httputil.ReverseProxy` (Peter, 2026-11-09), with its own `http.Transport`. It copies the request once and never changes the app's own request. It makes the call to the vendor, with TLS origination when set, and times each phase with `httptrace` (DNS, connect, TLS, first byte). On failure it answers with `502` or `504` and `Proxy-Status`.
3. **Body tap:** wraps the request and response bodies, passes every byte straight on, and keeps a copy of at most 1 MiB for later.

**After the reply has gone back:**

4. **Checker workers:** a fixed number of background workers fed by a bounded queue. Each takes a finished call and checks the bodies against the spec, redacts, makes the fingerprints and builds the record. If the queue is full, the call is recorded with `hotline.schema.skipped=overloaded` instead of waiting.
5. **Exporters:** one bounded queue and sender per destination. When a queue is full the oldest record is dropped and counted.

**Around the call:**

6. **Config loader:** reads and checks the config, specs and key files, watches them for changes, and swaps in a new immutable snapshot only when everything is valid. The same checks back `hotline-proxy validate`.
7. **Admin server:** `/livez` and `/readyz` on the admin port only.
8. **Own health data:** calls in flight, queue lengths, dropped records, config version, sent only to the `telemetry` exporter.

**Ports** (interfaces the core uses; each has a real adapter and a test fake): a record sink (exporters), a config source (files), a clock, and the vendor transport (`http.RoundTripper`). Add a new port only when a test needs to replace something real.

`main` builds the adapters, connects them to the core, starts everything, and on shutdown stops in reverse order: stop accepting calls, let calls in flight finish, drain the checker queue, flush the exporters, then exit.

## 4. Layout and packages

- Proxy code lives under `src/hotline-proxy/` in module `hotline-proxy`; the `proxy` package sits at the top of that folder, next to `cmd/`, `features/`, `internal/` and `testdata/`. The metrics code stays in `src/hotline/metrics/`, module `hotline`. The program entry point goes in `src/hotline-proxy/cmd/`; build it with `-o hotline-proxy` so the program isn't named `cmd`; it only reads flags, wires parts together and starts. All logic lives in packages it imports.
- One package per idea, named after what it provides, short and lower case: e.g. `ingress`, `forward`, `tap`, `check`, `redact`, `fingerprint`, `record`, `export`, `config`, `admin`. Never `util`, `common` or `helpers`.
- Packages depend in one direction: the forwarding path knows nothing about exporters; it hands a finished record to a small interface.
- Keep interfaces small (one or two methods) and declare them where they are *used*, not where they are implemented. Return concrete types.
- No package-level variables except sentinel errors, and no `init()` (the linter enforces this). Everything is passed in through constructors.

## 5. Errors

- Define sentinel errors per package: `var ErrUnknownIngress = errors.New("ingress: unknown ingress")`. Add detail with `fmt.Errorf("%w: ...", ErrX, ...)`. Check with `errors.Is` or `errors.As`. The linter forbids ad hoc `errors.New` inside functions.
- Errors carry context, never secrets: no header values, keys, tokens or body bytes in error text or logs.
- Failures talking to the vendor become a `502` or `504` answer with an RFC 9209 `Proxy-Status` header (e.g. `hotline; error=dns_timeout`) and the same name in the record's `error.type`.
- Never `panic` on input from apps, vendors, config or specs. A bad config at reload keeps the old config and reports why.

## 6. The call path: hard rules

These protect the customer's traffic. Breaking one is a bug even if tests pass.

- **Never slow or change the call.** Bodies stream through untouched. Body checks, fingerprints and exports run *after* the reply has gone back, off the call's goroutine.
- **Body copy is capped at 1 MiB** per call. Above it, the call passes and the record says `hotline.schema.skipped=body_too_large`.
- **Under load, skip, don't wait.** If checks fall behind, skip them (`hotline.schema.skipped=overloaded`). Export queues are bounded (default 10 000); when full, drop the oldest and count it. Never block on a full queue.
- **No retries.** The app's own retry logic is in charge.
- **Streaming and WebSockets:** pass each piece on at once (flush), never buffer the whole reply.
- **Always check the vendor's TLS certificate.** No setting turns it off.
- **Remove `Proxy-Authorization`** before calling the vendor. `Authorization`, cookies and secrets are never recorded.
- **Admin endpoints only on the admin port**, never on the proxy port.
- **Measure, don't judge.** The proxy records facts; good or bad is decided by the control plane.

## 7. Concurrency

- Every goroutine has an owner and a way to stop: it ends when its `context` is cancelled, and the owner waits for it (`sync.WaitGroup`) on shutdown.
- Pass `context.Context` as the first argument; never store it in a struct.
- The vendor call uses its own context bound by `vendorTimeout`, not the app's request context: if the app gives up, keep waiting and set `hotline.client.cancelled=true`.
- Share config by swapping an immutable snapshot (`atomic.Pointer`), so calls in flight finish on the config they started with.
- Pick the simplest tool: a channel to hand work to another goroutine, a mutex to guard shared fields, `sync/atomic` for counters and flags.
- Only the sender closes a channel. Give channel parameters a direction (`chan<-` or `<-chan`).
- Every `select` that waits also watches `ctx.Done()`, so nothing hangs after shutdown.
- Never start an unbounded number of goroutines per call; the checker workers are a fixed pool.
- In loops that wait repeatedly, reuse one `time.Timer` instead of calling `time.After` each time.
- Run tests with `-race` when touching concurrent code, and check that no goroutines are left running after a test with `go.uber.org/goleak` (`goleak.VerifyNone` in an `AfterEach`, or `goleak.VerifyTestMain`).
- Test timing through the injected clock, not with real sleeps.

## 8. OpenTelemetry

- Use the standard names from `go.opentelemetry.io/otel/semconv` constants wherever one exists. Hotline-only fields use the `hotline.` prefix and match the spec's tables exactly.
- One log record per call (`event.name = hotline.request`), the `hotline.requests` counter, optional spans. Data for customers and the proxy's own health data go to separate exporters, never mixed.
- Use `log/slog` for the proxy's own logs. When tracing is on, route it through the OpenTelemetry slog bridge (`go.opentelemetry.io/contrib/bridges/otelslog`) so log lines carry the trace and span IDs.

## 9. Before you hand work back

- `go test ./...` passes and `make lint` is clean.
- The test cases were listed first, and each new behaviour has a test that failed first.
- Any new module is named and justified.
- No secret or body value can reach a log, error or record.

### When asking Peter a question

While agreeing a chapter's requirements, ask one question at a time, and always bring its impact:
- Give the real situations it plays out in, such as a bad config edit, a rollout, a sidecar or a vendor that is down, and what happens in each for the apps and their vendor calls.
- Say what each choice costs and which one you recommend, and why.
- Keep it simple, everyday language a non-expert can follow: short sentences, no arrow chains, and every piece of jargon explained in plain words.

### When the pull request is merged

Mark the chapter as done everywhere it is tracked, in the same session:
- The chapter's requirement file in the project files (for example `connect-interception/requirements.md`): change its status line to say it is built and merged, with the pull request number and the `docs/` file where the detail now lives. Take "(draft)" out of the title.
- `memory.md`: add the chapter to the "Built" line of the slice plan, and its full entry, with the pull request and the `docs/` file, to `docs/decisions/slice-plan.md`.
- `proxy-spec.md` in `hotline-strategic`: if it still lists the chapter as "Later" or not built, open a pull request there to fix it.

## 10. Reviewing proxy code

Use this when asked to review a change, your own or someone else's. For a plain bug hunt, the built-in `/code-review` command works well too; this section adds what only this repo knows.

### How to review

1. Run `gofmt -l`, `go vet ./...` and `make lint` first, so the review is not spent on what tools already catch.
2. Read the `go` line in `go.mod`, and only give advice that fits that Go version.
3. Read the whole function around each change, not only the changed lines.
4. Go through the checks below. Skip a group that the change does not touch.
5. Before reporting, read the code again for every finding. Drop any finding you cannot point to a line for, and any that is only a different taste rather than a mistake.
6. Report findings in three groups: **Must fix** (breaks a rule or a behaviour), **Should fix** (makes the code harder to change or read), **Nit** (small style points). Each finding names the file and line, the rule, and why it matters.

### Tests (section 1)

- The feature file came first, and each new behaviour has a scenario named after what the proxy does.
- Tests drive the proxy from outside: config, the proxy port, fake vendors and the fake OTLP receiver. No step calls internal functions or reads internal fields.
- No production code was changed only for a test: nothing made public for a test, no return value added only to check it, no function only a test calls, no test mode.
- Expected values are written out in full; no constants from the code, no recomputed logic, no snapshot comparisons.
- Fakes sit only at the edges. No mocks of one part of the proxy to test another, no testify, no mocking libraries.
- No new test file that mirrors one source file or one function. A new solitary Ginkgo test meets the rule: stable interface and many cases of its own.
- No real sleeps (the clock is injected), no calls to the real internet, and goroutine leak checks where goroutines start.

### The call path (section 6)

- Nothing slows or changes the call: no check, fingerprint or export runs on the call's goroutine.
- Body copies stop at 1 MiB; queues are bounded and drop instead of block; there are no retries.
- Streaming replies are flushed piece by piece.
- TLS certificates are always checked; `Proxy-Authorization` is removed before the vendor call.
- No secret, header value or body byte can reach a log, error text or record.

### Go code (sections 2, 4, 5 and 7)

- Every new outside module is named in the pull request, needed, and has a license that fits Apache-2.0.
- Packages are named after what they provide; no `util` or `common`; no package-level variables except sentinel errors; no `init()`.
- Interfaces are small and declared where they are used; constructors return concrete types; names are private unless needed outside.
- Errors use the package's sentinel errors with `%w`; error strings are lower case with no full stop at the end; no `panic` on outside input; no error is thrown away with `_` without a reason.
- Every goroutine has an owner and stops on `ctx.Done()`; `context.Context` comes first and is never stored in a struct; only the sender closes a channel.
- Logs use `log/slog` with a fixed message and key-value fields, not `fmt.Sprintf` messages.
- Naming follows Go habits: `MixedCaps`, initialisms kept in one case (`URL`, `ID`, `HTTP`), short receiver names used the same way in every method, no package name repeated in a type name (`ingress.Finder`, not `ingress.IngressFinder`).
- Opinions about OpenTelemetry names are checked against `semconv` and the spec's tables (section 8), not memory.

## Credits

Parts of sections 3 and 7 (ports and adapters, goroutine and channel rules, goroutine leak checks, the slog bridge) are adapted, in our own words, from [samber/cc-skills-golang](https://github.com/samber/cc-skills-golang) by Samuel Berthe (MIT License).

Section 10 is written in our own words, with ideas taken from two Apache-2.0 skills: the review checklist in [cxuu/golang-skills](https://github.com/cxuu/golang-skills) (`go-code-review`, built from the Google and Uber Go style guides and the Go Wiki CodeReviewComments page), and the review steps in [existential-birds/beagle](https://github.com/existential-birds/beagle) (`go-code-review`: read the Go version first, read whole functions, and check every finding before reporting it).
