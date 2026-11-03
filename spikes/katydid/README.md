# Katydid spike

Throwaway spike: check JSON and XML bodies against one JSON Schema with katydid. Not wired into the proxy. Findings: the project file `katydid-sole-engine/findings.md`.

- `checker.go`: `Compile`, `CheckJSON`, `CheckXML`. A loaded katydid matcher is not safe to share, so each check takes a lock.
- `xml.go`: reads XML with Go's standard reader and builds katydid's tree by the schema (repeated elements to arrays, typed text only where the schema says, `xmlns` dropped, `<!DOCTYPE` refused).
- Run tests: `GOWORK=off go test -race ./...` (the repo `go.work` is not used by this module). Benchmarks: `GOWORK=off go test -run xxx -bench .`
