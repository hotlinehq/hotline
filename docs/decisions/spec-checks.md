# Spec checks and shared apis decisions (Peter, 2026-10-29)

Full wording in [docs/proxy-spec-checks.md](../proxy-spec-checks.md).

1. Every part of a call is checked against the spec, not only the body: path, query, headers, cookies and body; in the reply, status, headers and body.
2. Unknown query values are reported; unknown headers are ignored; extra body fields follow `additionalProperties`.
3. Each result is structured: `part`, `place`, `rule`, `expected`. Never a value from the call.
4. A content type the spec doesn't list, or a body where none is expected, is reported.
5. A reply status the spec doesn't list (no `default`) is reported, and that reply is not checked further.
6. At most 20 results per side, plus `errors_total` when more.
7. Common formats are checked (`date-time`, `date`, `email`, `uuid`, `uri`, `ipv4`, `ipv6`, `int32`, `int64`).
8. The login key from the security section must be present; its value is never read.
9. Only OpenAPI 3.1; `$ref` within the same file; OpenAPI query styles; Stripe's bracket form bodies.
10. A broken spec is a config problem: refused at start, by `validate` and on reload.
11. One spec is read once and shared by every integration that uses it.
12. The config names each vendor api once in a top-level `apis` section; integrations name the apis they use, and may change only `vendorTimeout`. `not-passed` keeps its settings on itself.
13. The JSON Schema library sits behind `RequestCheck` and `ResponseCheck` in `internal/spec/schemacheck.go` (Peter, 2026-11-05). They take the request and reply as Go's http server gives them, unparsed: escaped path, raw query, `http.Header`, and the body's bytes. The checks that need no schema (route, body listed or required, content type, too large) are made before them. Only the `santhosh_*.go` files know santhosh, so another engine such as katydid can take its place. Whether `Body` keeps returning the marked fields is decided with the katydid work.
