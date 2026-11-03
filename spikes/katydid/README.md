# Katydid spike

Throwaway spike: check JSON and XML bodies against one JSON Schema with katydid. Not wired into the proxy. Findings: the project file `katydid-sole-engine/findings.md`.

- `checker.go`: `Compile`, `CheckJSON`, `CheckXML`. A loaded katydid matcher is not safe to share, so each check takes a lock.
- `tree.go`: reads XML with Go's standard reader into a plain element tree (refuses `<!DOCTYPE`, drops `xmlns`).
- `xml.go`: builds katydid's tree by the schema (repeated elements to arrays, typed text only where the schema says, `xmlns` dropped, `<!DOCTYPE` refused).
- `stream.go`: the streaming XML reader (`CheckXMLStream`). Reads the bytes piece by piece straight into katydid's matcher, no tree, nothing copied from the body. Limits: repeated elements that are not side by side are an error; schema choices (`allOf`, `oneOf`, `anyOf`) are read through their first member that names a type or property.
- Run tests: `GOWORK=off go test -race ./...` (the repo `go.work` is not used by this module). Benchmarks: `GOWORK=off go test -run xxx -bench .`

## W3C conformance cases

`testdata/xmlconf_manifest.py` cuts the W3C XML Conformance Test Suite down to the cases we can read (XML 1.0 fifth edition, UTF-8, no DOCTYPE, no entity declarations, namespace rules left out because we ignore namespaces). Download the suite from https://www.w3.org/XML/Test/ (`xmlts20130923.tar.gz`), then:

```
tar -xzf xmlts20130923.tar.gz
python3 testdata/xmlconf_manifest.py xmlconf manifest.json
XMLCONF_MANIFEST=$PWD/manifest.json GOWORK=off go test -v -ginkgo.focus conformance ./...
```

The streaming reader must get all of them right; the tree reader is only reported.
