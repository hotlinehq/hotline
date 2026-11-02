# Hotline Project

Read [memory.md](memory.md) first: it is a short index of decisions already made, the slice plan, and where the detail lives. Add new decisions to the matching file in `docs/decisions/`, and keep memory.md short.

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
