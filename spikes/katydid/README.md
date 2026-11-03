# Katydid spike

Throwaway spike: check JSON and XML bodies against one JSON Schema with katydid. Not wired into the proxy. Findings: the project file `katydid-sole-engine/findings.md`.

- `checker.go`: `Compile`, `CheckJSON`, `CheckXML`. A loaded katydid matcher is not safe to share, so each check takes a lock.
- `tree.go`: reads XML with Go's standard reader into a plain element tree (refuses `<!DOCTYPE`, drops `xmlns`).
- `xml.go`: builds katydid's tree by the schema (repeated elements to arrays, typed text only where the schema says, `xmlns` dropped, `<!DOCTYPE` refused).
- `stream.go`: the streaming XML reader (`CheckXMLStream`). Reads the bytes piece by piece straight into katydid's matcher, no tree, nothing copied from the body. Limits: repeated elements that are not side by side are an error; schema choices (`allOf`, `oneOf`, `anyOf`) are read through their first member that names a type or property.
- Run tests: `GOWORK=off go test -race ./...` (the repo `go.work` is not used by this module). Benchmarks: `GOWORK=off go test -run xxx -bench .`
