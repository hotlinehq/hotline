# XML bodies and S3: requirements

*Agreed with Peter, 2026-10-31; built the same day. Scenarios: `src/hotline-proxy/features/xml-bodies.feature`, `query-word-routes.feature` and `aws-s3.feature`. The worked example is [examples/aws-shop](../examples/aws-shop/README.md), named `aws-sqs-invoices` until 2026-11-01.*

One step, driven by S3 in the invoice example (Peter, 2026-10-31). It builds three things together: XML bodies for any vendor, S3 routes, and the S3 part of the invoice story.

## 1. XML bodies, for any vendor

1. **Read by the spec's shape** (Peter, 2026-10-31). The proxy reads an XML body into the same tree it gets from JSON, following the schema and its OpenAPI `xml` notes: `name` (the element's name), `attribute` (a value written inside the tag), `wrapped` (a list with an outer element). Without notes, OpenAPI's defaults apply: an element is named after its field, and a list is its item element repeated. XML is not only for AWS: any vendor's spec can use it.
2. **Then the same checks as JSON.** Same rules, same results, same limit of 20, same `errors_total`, same body-field copying with `x-hotline-record`. Requests and replies both.
3. **Text becomes typed** where the schema asks: `"12"` becomes the number 12, `"true"` becomes true, as form bodies do. A list stays a list even with one item.
4. **Places look like JSON's:** `body $.Contents[1].Size required`. Never a value from the call.
5. **Which bodies are XML.** `application/xml`, `text/xml` and any type ending in `+xml`. Any other type is XML only where the spec marks it `x-hotline-body: xml`, as `x-hotline-body: json` does for AWS's JSON type. The mark takes `json` or `xml`; anything else refuses the spec.
6. **New results.**
   - `body $ xml`: the body isn't well-formed XML (broken, cut off, two root elements, or a `<!DOCTYPE`).
   - `body $ xml_root <name>`: the root element isn't the one the schema names, such as `<Error>` where `<ListBucketResult>` was expected.
7. **Ignored on purpose.**
   - Namespaces (the `xmlns="…"` web addresses): elements match by their name only.
   - Elements the schema doesn't name, unless the schema says `additionalProperties: false`; then each is reported, like an extra JSON field.
   - Comments, and the `<?xml …?>` line at the top.
8. **Safe reading.** A body with `<!DOCTYPE` (the part of XML that defines shortcuts, used to blow a tiny body up to gigabytes) is `body $ xml` and isn't read further. The proxy never fetches anything an XML body points to.
9. **Size.** XML follows the body size limit; a larger body is skipped as `body_too_large`.
   **Depth.** An element nested more than 64 deep, the root counted, makes the body `body $ xml`. Each level costs memory as it is read, so without a limit a 2 MB body nested very deep took about 180 MB (security scan, 2026-11-18).
10. **No new library.** Go's own XML reader.

## 2. Routes picked by a query word

1. **Every S3 action is its own route,** like SQS. The action is picked by the method, the path, and sometimes a word in the query, such as `POST /?delete` for deleting many files.
2. **The query word goes after `#`** (Peter, 2026-10-31), in the same spot as SQS's header: `/#?delete`, `/{Key+}#?tagging`, `/{Key+}#?x-id=GetObject`. Several words are joined with `&`. A word with a value matches only that value. A route with words matches only when the call's query has them, and beats the same route without words.
3. **The word is part of the route,** so it is never reported as an unknown query value, and a fixed value stays in `url.full`.
4. **`{name+}` takes the rest of the path** (Peter, 2026-10-31), slashes included, as AWS writes S3 file names (`2026/A-1001.pdf`). Only as the last part of a path; elsewhere it refuses the spec. It is checked and redacted as one value. A fixed part beats a value, and a value beats the rest.

## 3. The S3 spec

1. **`tools/aws-openapi` learns AWS's REST and XML form** (AWS calls it `restXml`): routes from each action's method, path and query word; values in the path, query and headers; `xml` notes from AWS's own tags (`xmlName`, `xmlAttribute`, `xmlFlattened`); and `x-hotline-body: xml` on every XML body.
2. **One stable spec,** `specs/aws/s3/2006-03-01.openapi.yaml`, with a test that keeps it in step with the tool. Detail: [specs/aws/s3/README.md](../specs/aws/s3/README.md).
3. **The bucket is in the host name** (`shop-invoices.s3.eu-west-1.amazonaws.com`), so the api names that host, and routes have no bucket.
4. **`x-id`** (the action name AWS's SDKs add to some calls) is left out of a route unless two actions would share a route without it.
5. **AWS's error reply** (`<Error><Code>…`) is in the spec for every action's 4xx and 5xx, so it is checked like any reply.
6. **File bodies are passed, never read.** They are listed as `*/*` with no schema, so their records say `skipped=unsupported_content_type`.

## 4. S3 in the invoice story

1. **The worker stores** each PDF in the bucket `shop-invoices` as `A-1001.pdf` (`PutObject`), in place of a local folder.
2. **The shop fetches it** (`fetch`, `GetObject`). A missing file answers `404` with `<Error><Code>NoSuchKey</Code>`, checked as a normal error reply.
3. **The shop lists** the invoices (`list`, `ListObjectsV2`): the XML reply is checked (Peter, 2026-10-31).
4. **The shop deletes old invoices** in one call (`cleanup --before`, `DeleteObjects`): the XML request is checked (Peter, 2026-10-31).
5. **One overlay for the bucket**, `src/hotline-proxy/testdata/aws/invoices-bucket.overlay.yaml`, pins `Name` to `shop-invoices` in the list reply, and file names to `^[A-Z]-[0-9]+\.pdf$`.
6. **Signed calls stay valid,** as for SQS. That includes uploads signed in parts (`aws-chunked`) with a checksum sent after the body, which the AWS SDK for Go uses by default.

The S3 scenarios run the example app through the proxy in the test, with a fake network: the bucket is in the host name, which can't point at a local program without changing the computer's name lookup.

## Not in this step

- SOAP, and turning WSDL into OpenAPI (Peter, 2026-10-31).
- SNS and STS: they can reuse this XML reading in their own step, with the routes named by a body field.
- S3 actions picked by a header (copying a file is a `PUT` with `x-amz-copy-source`).
- S3 addresses with the bucket in the path; the Go SDK uses the host form.
- Blocking calls that fail a check.
