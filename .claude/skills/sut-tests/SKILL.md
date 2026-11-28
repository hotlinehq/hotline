---
name: sut-tests
description: Shape every Go test in this repo around a SUT struct (Peter's pattern). Use when writing, changing or reviewing any Ginkgo test or godog step in hotline or hotline-proxy.
---

# Tests shaped around a SUT struct

SUT means system under test: the part a test drives and checks. Here it is also a small struct that owns all setup, so each test reads in a few lines. Reference: `git show 668dfa9:src/hotline/servicelevels/pipelne_test.go`. Decision: [docs/decisions/sut-tests.md](../../../docs/decisions/sut-tests.md).

**Where it applies:** every Ginkgo test and every godog step. Not to fuzz tests, benchmarks, or a `DescribeTable` whose rows each call one function with a value and check what comes back.

## Ginkgo tests

1. One `sut` per `Describe`, or per `Context` when contexts need different setups: `sut := pipelineSUT{}`. The type is always named `<area>SUT`. Close it with `DeferCleanup(sut.Close)` right after it is built, so a failed test leaves no server or file open.
2. The struct and its methods go at the bottom of the same file. Fields are the real parts and the fakes at the edge (manual clock, in-memory store, fake vendor).
3. Builders start with `for` and give a working system with defaults: `forPipeline()`, `forEmptyQueue()`, `forFullDisk()`.
4. Actions and questions use domain words: `IngestOKRequest()`, `SendCall()`, `Report()`, `RecordsSent()`. Never `doStep2` or the name of an internal function.
5. Defaults live in the sut. Pass a value only when it is the point of the test. Add variants by name (`SendCallTo(path)`), the short form calling the longer one.
6. Setup errors are checked inside the sut with `Expect(err).WithOffset(1).NotTo(HaveOccurred())`, so a failure points at the test's line, not the sut's. Return an error only when the error is what the test checks.
7. Waiting, locking and polling live in the sut. A test never touches a mutex, a channel or a field of the sut.
8. The test body is build, act, check: usually three to eight lines, and the `Expect` lines stay in the test.
9. Setup closures and setup helpers in a test file become sut methods. Shared fakes and the shared wait and port helpers (`eventually`, `pause`, `reservedPorts`, see `docs/flakes.md`) stay as they are, and the sut uses them.
10. The sut uses only the ways in and out that the `hotline-go-proxy` skill allows: no code constants, no exports made for tests, expected values written out in full.

## godog steps

1. One sut per area (for example capture, ledger, console, exporters), named `<area>SUT`, each with a constructor that takes only what it needs, its own fakes, `AddSteps(sc)` for its own steps, and `Close()`.
2. A small root sut owns the proxy and the shared pieces (ports, clock, config file). Area suts get them through their constructor, not by reaching into one shared struct.
3. `initializeScenario` builds the suts, calls each `AddSteps`, and closes them all in `After`.
4. godog steps fail by returning an error, so area sut methods return `error` rather than call `Expect`, the way the steps do today.

## Review checklist

- Does every `It` read in a few lines, with no setup, error plumbing or waiting in it?
- Is there one sut per `Describe` or `Context`, with `for...` builders?
- Do sut method names say what happens in domain words?
- Does a godog step reach into state that belongs to another area?
