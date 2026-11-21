# AWS through the proxy, starting with SQS: requirements

*Agreed with Peter, 2026-10-29; built 2026-10-30. Scenarios: `src/hotline-proxy/features/aws-sqs.feature` and `end-to-end-aws.feature`. The source of truth is `proxy-spec.md` in the `hotline-strategic` repo. The worked example is [examples/aws-shop](../examples/aws-shop/README.md), named `aws-sqs-invoices` until 2026-11-01; the message check it uses is in [proxy-sqs-example.md](proxy-sqs-example.md).*

## What the feature is

AWS signs every request with Signature Version 4 (SigV4). The SDK mixes the method, the path, the query, `Host`, a list of headers and a hash of the body with the app's secret key, and puts the result in `Authorization`. AWS redoes the sum and answers `403` if any of those parts changed. The proxy holds no AWS keys and cannot sign again.

Every SQS call is `POST /`. Which action it is (send, receive, delete) is named only in the `X-Amz-Target` header, for example `AmazonSQS.SendMessage`. This is AWS's JSON protocol, which SQS has used since November 2023.

What we checked with the real SDK (`service/sqs` v1.52.7):

- **Forward proxy works** when the app's SQS address is `http://sqs.eu-west-1.amazonaws.com` and the proxy is `http://shop-a:x@hotline:8080`. Go adds `Proxy-Authorization` after signing. With the default `https` address, Go asks for a `CONNECT` tunnel and gets `501`.
- **Host header and transparent redirect work** when the app keeps AWS's own name and only changes how it connects (a custom dialer, or `hostAliases`).
- **Vendor URL in path cannot work.** The SDK signs the proxy's own name and the path `/https://…`, which the proxy has to change.
- **The integration ID must be added after signing.** Added through the SDK, it is signed; the proxy removes it, and AWS answers `403`.

## Requirements

1. **Signed calls stay valid** (Peter, 2026-10-29). The proxy forwards the method, the path and query as sent, `Host`, the body byte for byte, `Content-Length`, and every end-to-end header with its exact value. It only removes `Proxy-Authorization` and the headers for one hop (such as `Connection`), and may switch `http` to `https`, since the scheme is never signed. A body with a known length is never sent to the vendor in chunks of unknown size, also over HTTP/2. The proxy already behaves this way; this makes it a rule with a test.
2. **The integration ID stays in `Proxy-Authorization`** (Peter, 2026-10-29). The proxy never reads `Authorization`, for security reasons. A port per integration is ruled out, since integrations are unlimited.
3. **Each action is its own route in the spec** (Peter, 2026-10-29). AWS publishes no OpenAPI for SQS, only a Smithy model (AWS's own API description language), and Smithy's OpenAPI converter does not handle SQS's protocol. So we generate our own OpenAPI spec (3.2 since 2026-11-21) from the Smithy model. OpenAPI allows one operation per path and method, so each action's route names the header value after `#`, the form the `aws2openapi` converter uses: `/#X-Amz-Target=AmazonSQS.SendMessage`.
4. **The action header is named in the spec** (Peter, 2026-10-29). A mark at the top of our spec, `x-hotline-action-header: X-Amz-Target`, says which header names the action. It belongs to the api, never to an integration, is never hard-coded in the proxy, and the config does not change. Like `x-hotline-redact`, it lives only in our copy of the spec. In the same way, each body's media type is marked `x-hotline-body: json`, so the proxy reads `application/x-amz-json-1.0` as JSON without knowing anything about AWS (Peter, 2026-10-30).
5. **Matching a route with `#`.** A route `/#X-Amz-Target=AmazonSQS.SendMessage` matches a call to `/` whose `X-Amz-Target` header is exactly `AmazonSQS.SendMessage`. With the header missing or unknown, the call has no route (`no_spec_route`). The record's `url.template` is the route as written, so each action gets its own line.
6. **AWS headers in the record come from our spec** (Peter, 2026-10-29). Our SQS spec marks `x-amzn-RequestId` (AWS's request ID) and `x-amzn-query-error` (the error name) with `x-hotline-redact: false`. The default header list for all vendors does not change. `X-Amz-Target` need not be recorded, since the action is in the route.
7. **No patterns in `hosts`** (Peter, 2026-10-29). Each region's name is listed (`sqs.eu-west-1.amazonaws.com`, `sqs.eu-central-1.amazonaws.com`, …), as for any vendor.
8. **The setup guide for AWS** (Peter, 2026-10-29):
   - recommends the forward proxy, set only on the SQS client, with the app's own HTTP client turning `https` into `http` after the SDK has signed (Peter, 2026-10-29). The SDK keeps its normal address and region, and the scheme is not signed. Tested with the real SDK. The Go setup:
     ```go
     type toHTTP struct{ next http.RoundTripper }

     func (t toHTTP) RoundTrip(r *http.Request) (*http.Response, error) {
         r = r.Clone(r.Context())
         r.URL.Scheme = "http" // after signing; the scheme is not signed
         return t.next.RoundTrip(r)
     }

     proxy, _ := url.Parse("http://shop-a:x@hotline:8080")
     transport := http.DefaultTransport.(*http.Transport).Clone()
     transport.Proxy = http.ProxyURL(proxy)

     client := sqs.NewFromConfig(cfg, func(o *sqs.Options) {
         o.HTTPClient = &http.Client{Transport: toHTTP{transport}}
     })
     ```
     Setting `BaseEndpoint` to `http://sqs.eu-west-1.amazonaws.com`, or `HTTP_PROXY` with `NO_PROXY`, also works. `HTTPS_PROXY` with the normal `https` address does not: Go asks for a `CONNECT` tunnel and gets `501`;
   - says plainly that the vendor URL in path ingress cannot work for vendors that sign the host, such as AWS;
   - says the integration ID must be added after signing (by Go's proxy support, or a wrapper around the HTTP client), never through the SDK's own header options;
   - warns that setting `HTTP_PROXY` for the whole app also sends the SDK's own key lookups (`169.254.169.254`, `169.254.170.2`, `169.254.170.23`) to the proxy, where the internal address guard refuses them, so those go in `NO_PROXY`;
   - says that SQS reached through a VPC endpoint (a private entrance to AWS inside the customer's network) has internal IPs, so the api needs `allowInternalIPs: true`;
   - says `vendorTimeout` must stay above 20 seconds, since long polling keeps AWS silent for up to 20 seconds on purpose.

## Tests

### Signed calls stay valid (`aws-sqs.feature`)

The test uses a fixed, recorded request, signed once outside the repo with the SDK's signer, a made-up key and a fixed time: `testdata/aws/sqs-sendmessage-signed.http`. Hotline never imports the AWS SDK (Peter, 2026-10-29). The fake vendor needs no signing code: a signature stays valid exactly when every signed part arrives unchanged, so it checks the method, path, `Host`, body, and each header named in the fixture's `SignedHeaders`, read from its own `Authorization` line.

```gherkin
  Scenario Outline: A signed call reaches the vendor with every signed part unchanged
    The fixture was signed once by the AWS SDK: Host, path, body hash,
    Content-Length, X-Amz-Date, X-Amz-Target and the SDK's own headers.
    A signature stays valid when every signed part arrives unchanged
    (Peter, 2026-10-29).
    Given the recorded signed SQS call from the fixture, with Proxy-Authorization added after signing
    When the app sends it to the proxy over <ingress> with <protocol>
    Then every signed part reaches the vendor exactly as in the fixture
    And Proxy-Authorization is removed
    And the vendor receives Content-Length with the body's length, not a body in chunks

    Examples:
      | ingress           | protocol     |
      | the forward proxy | HTTP/1.1     |
      | the Host header   | HTTP/1.1     |
      | the Host header   | plain HTTP/2 |
```

### Routes named by a header (`aws-sqs.feature`)

- A call to `/` with `X-Amz-Target: AmazonSQS.SendMessage` matches `/#X-Amz-Target=AmazonSQS.SendMessage`, is checked against that route's body schema, and its record has that `url.template`.
- The same call with `X-Amz-Target: AmazonSQS.ReceiveMessage` matches the receive route instead.
- A call with no `X-Amz-Target`, or a value with no route, is skipped with `no_spec_route` and has no `url.template`.
- A spec with routes ending `#Name=value` but no `x-hotline-action-header`, or naming another header than the routes, is a broken spec: refused at start and by `validate`.

## Not in this step

- SQS's older query protocol (a form body with `Action=SendMessage`), sent by SDKs from before late 2023.
- Other AWS services. S3 came next, on AWS's REST and XML protocol: see [proxy-xml-bodies.md](proxy-xml-bodies.md).
- `CONNECT` with interception, the only way an unchanged SDK with its `https` address would work.
