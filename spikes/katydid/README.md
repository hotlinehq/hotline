# Katydid spike

Throwaway spike: check JSON and XML bodies against one JSON Schema with katydid. Not wired into the proxy. Findings: the project file `katydid-sole-engine/findings.md`.

- `checker.go`: `Compile`, `CheckJSON`, `CheckXML`. A loaded katydid matcher is not safe to share, so each check takes a lock.
- `explain.go`: `WhyJSON` and `WhyXML` return a `*Failure` with place (`$.Contents[1].Size`), rule and expected, like the proxy reports today. Katydid gives only the path; the rule is rebuilt from the schema and the body, only for failing bodies. Tests compare it with santhosh. Not explained yet: allOf, oneOf, anyOf, not, format.
- `schema.go`: reads the JSON Schema as plain maps: follows `$ref`, finds a property's schema, and gives its type.
- `xml_streaming_parser.go`: the XML reader (`CheckXML`). Reads the bytes piece by piece straight into katydid's matcher, no tree, nothing copied from the body. Limits: repeated elements that are not side by side are an error; schema choices (`allOf`, `oneOf`, `anyOf`) are read through their first member that names a type or property.
- Run tests: `GOWORK=off go test -race ./...`. Benchmarks: `GOWORK=off go test -run xxx -bench .`

## W3C conformance cases

`conformance_test.go` runs the W3C XML Conformance Test Suite on the XML reader. It reads the suite's index files itself and leaves out the cases we cannot or will not read (DOCTYPE, entity declarations, XML 1.1, other encodings, namespace rules). Download the suite from https://www.w3.org/XML/Test/ (`xmlts20130923.tar.gz`), then:

```
tar -xzf xmlts20130923.tar.gz
XMLCONF_DIR=$PWD/xmlconf GOWORK=off go test -v -ginkgo.focus conformance ./...
```

The reader must get all of them right.
