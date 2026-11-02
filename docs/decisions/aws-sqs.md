# AWS and SQS decisions (Peter, 2026-10-29)

Full wording in [docs/proxy-aws.md](../proxy-aws.md).

1. Signed calls stay valid: the proxy forwards `Host`, the path, the body, `Content-Length` and the app's headers unchanged; a rule with a test.
2. The integration ID stays in `Proxy-Authorization`. The proxy never reads `Authorization` (security). No port per integration.
3. Each AWS action is its own route in a spec we generate: `/#X-Amz-Target=AmazonSQS.SendMessage`.
4. The action header is named in our spec (`x-hotline-action-header`), per api, never per integration or in the proxy's code.
5. The test uses a signed request fixture made once; Hotline never depends on the AWS SDK.
6. AWS headers to record are marked in our SQS spec; the default header list is unchanged. No patterns in `hosts`.
7. The setup guide recommends the forward proxy for AWS, with the app's HTTP client turning `https` into `http` after signing, says vendor URL in path can't work for signed hosts, and warns about `NO_PROXY` and VPC endpoints.
