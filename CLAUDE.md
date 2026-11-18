# Hotline Project

Read [memory.md](memory.md) first: it is a short index of decisions already made, the slice plan, and where the detail lives. Add new decisions to the matching file in `docs/decisions/`, and keep memory.md short.

## Asking Peter questions

Whenever you ask Peter to decide something (requirements, design, which option), use the decomplect skill's Interview mode (Peter, 2026-11-18):

- One question per message, numbered (Q1, Q2, ...). Wait for the answer, say in one line what is now decided, then ask the next.
- Context first: two to four plain sentences on what exists today and why the question comes up. Answer from the code and docs whatever they can answer; ask only what Peter alone can decide.
- Then the question in one sentence, your pick with a one-sentence reason, and a small tradeoff table: today's behaviour as the first column, two to four rows of criteria, words in every cell, marked 🟢 good, 🟡 challenge, 🔴 blocking, ? unknown. No totals.
- Record each answer where the topic's decisions live.

## Development Approach

This project uses **Test-Driven Development (TDD)**. Write tests before writing implementation code.

## Testing

- **Proxy behaviour**: Gherkin feature files in `src/hotline-proxy/features/`, run by [godog](https://github.com/cucumber/godog). See the `hotline-go-proxy` skill for the system under test approach.
- **Everything else, and small solitary tests**: [Ginkgo](https://onsi.github.io/ginkgo/) + [Gomega](https://onsi.github.io/gomega/)
- **Run tests**: `go test ./...` from the relevant module directory

### Test structure

Each package has a suite bootstrap file (e.g. `suite_test.go`) that wires Ginkgo into `go test`:

```go
package mypkg_test

import (
    "testing"
    . "github.com/onsi/ginkgo/v2"
    . "github.com/onsi/gomega"
)

func TestMypkg(t *testing.T) {
    RegisterFailHandler(Fail)
    RunSpecs(t, "Mypkg Suite")
}
```

Tests live in `*_test.go` files using Ginkgo's `Describe`/`Context`/`It` blocks and Gomega matchers (`Expect(...).To(...)`).

### TDD workflow

1. Write a failing `It` block describing the behaviour.
2. Run `go test ./...` — confirm it fails.
3. Write the minimum implementation to make it pass.
4. Run `go test ./...` — confirm it passes.
5. Refactor, keeping tests green.
