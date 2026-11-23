# Glossary

Hotline is a proxy that sits between a company's apps and the outside APIs they pay for. It forwards every call unchanged, and on the side it checks the call against the vendor's spec, times it and writes down how it went, so the company can later show what the vendor really delivered.

## Who and what is involved

**Vendor**:
A company outside whose API the apps call over HTTP, such as Stripe or AWS.
_Avoid_: upstream, provider, third party

**App**:
A program of the customer's that calls a vendor through the proxy, usually without any change to its code.
_Avoid_: client, service, caller

**Operator**:
The person who writes the config and the vendor specs, and runs the proxy.
_Avoid_: admin, user

**Api**:
One vendor API as the config knows it: a name under `apis`, the vendor host names it covers, and the spec that describes it. Written once and shared by every integration that uses it.
_Avoid_: vendor api (in prose it is fine; in the config the word is `api`), endpoint, service

**Integration**:
One app's entry in the config: its integration ID, the apis it may call, and its own settings.
_Avoid_: tenant, client, app (the app is the program; the integration is its entry)

**Integration ID**:
The name an app sends in the user name part of `Proxy-Authorization: Basic`, which tells the proxy which integration a call belongs to.
_Avoid_: app ID, client ID, fingerprint key

**not-passed**:
The reserved integration for calls whose integration ID is missing or unknown, or whose host is outside the integration's apis. Its calls get a minimal record and no spec checks.
_Avoid_: unknown, default integration, anonymous

## Calls and how apps reach the proxy

**Call**:
One request from an app to a vendor and the vendor's response, as it passes through the proxy.
_Avoid_: request (that is only the app's half; the vendor's half is the response), transaction, exchange (the name of the spec package's type)

**Response**:
The vendor's answer to a call, the same word HTTP, Go, OpenTelemetry and OpenAPI use.
_Avoid_: reply, answer

**Ingress**:
A way an app reaches the proxy. There are six: forward proxy, vendor URL in path, Host header, CONNECT, transparent, and capture.
_Avoid_: door, entry point, mode

**Forward proxy ingress**:
The app puts the full vendor address in the request line, as with a classic HTTP proxy.
_Avoid_: HTTP proxy mode, plain proxy

**Vendor URL in path ingress**:
The app puts the whole vendor address after the proxy's own address, such as `/https://api.stripe.com/v1/...`.
_Avoid_: path ingress, URL rewrite

**Host header ingress**:
The app calls the proxy with a plain path and names the vendor in the `Host` header, or in `:authority` over HTTP/2.
_Avoid_: virtual host ingress

**CONNECT ingress**:
The app asks the proxy for a tunnel with `CONNECT`, as `HTTPS_PROXY` makes it do, and the proxy reads the calls inside by interception.
_Avoid_: tunnel ingress, HTTPS proxy

**Transparent ingress**:
Connections that a firewall or network rule sends to the proxy without the app knowing, which carry no `CONNECT`. Handled on a listener with the `transparent` part.
_Avoid_: intercept ingress, `listen.intercept` and the Go type `Intercept` (the old names), transparent redirect (the older plain-HTTP form of it)

**Capture ingress**:
Connections that the macOS helper, Hotline Capture, picks out of chosen Mac apps and hands to the proxy as a `CONNECT` over a socket file, with the capture headers. Handled on a listener with the `capture` part.
_Avoid_: Mac ingress, `listen.capture` (the old name)

**Hotline Capture**:
The small macOS app and system extension, built in its own repository, that does the picking out for the capture ingress.
_Avoid_: capture app, capture helper, the extension (alone)

**Capture headers**:
The `Hotline-Capture`, `Hotline-Capture-Pid` and `Hotline-Capture-Program` headers on a captured `CONNECT`: the version of the contract, and which program on the Mac made the connection. Believed only on a capture listener.
_Avoid_: capture metadata

**Interception**:
The proxy posing as the vendor inside a `CONNECT` tunnel or a redirected connection: it shows the app a certificate for the vendor's name, signed on the spot, so it can read each call, and opens its own TLS connection to the real vendor.
_Avoid_: man in the middle, decryption, intercept (as the name of an ingress)

**Passthrough**:
A host, or a whole connection, that the proxy relays untouched, without interception and without reading it, such as a host whose app pins its certificate. The record says `hotline.intercept.mode=passthrough`.
_Avoid_: bypass, skip, blind tunnel

**TLS origination**:
The proxy starting the encryption to the vendor itself, so an app may send plain `http` and the vendor still gets `https`. On by default.
_Avoid_: TLS upgrade, HTTPS rewrite

**Address guard**:
The check that refuses calls to loopback and private addresses unless an api sets `allowInternalIPs`.
_Avoid_: internal guard, SSRF filter, firewall

**WebSocket session**:
What follows a call that the vendor answered with `101 Switching Protocols`: messages both ways on the same connection. It gets a session record of its own when it ends.
_Avoid_: socket, stream

## Config and running the proxy

**Config**:
The one file, `hotline-proxy.yaml`, that says what the proxy listens on, which apis and integrations exist, and where records go.
_Avoid_: settings file, definitions

**Listener**:
A named port or socket file (a file on disk that programs on the same machine connect to) the proxy opens for apps, under `listeners`. Its parts (`tls`, `connect`, `transparent`, `capture`) say what it takes; with no part it is a forward proxy port.
_Avoid_: port (a listener may be a socket file), `listen` (the old section), endpoint, Unix socket

**Admin port**:
The proxy's own port for health checks (`/livez`, `/readyz`) and profile pages, set in the `admin` section. Never a listener, so apps cannot reach it by accident.
_Avoid_: management port, metrics port

**Cert**:
A named certificate and key pair under `certs`. Where it is used, `mode` says whether it is shown as it is (`serve`) or used as a certificate authority (`sign`): a certificate whose key signs a new certificate for each vendor name, which the apps must trust.
_Avoid_: issuer, in both its meanings: our signing pair (the old `issuers` section) and the authority that signed a vendor's certificate. Say "certificate authority", with "our" or "the vendor's" in front. Also avoid: CA cert, keypair

**Config reload**:
Reading the config, specs, overlays, keys and certs again while running, every few seconds or at once on a `HUP` signal, without dropping calls.
_Avoid_: hot reload, refresh, restart

**Own telemetry**:
Numbers about the proxy itself (calls in flight, queue lengths, drops), sent only when the `telemetry` section names a collector.
_Avoid_: proxy metrics, internal metrics, self-monitoring, "the `telemetry` destination" (say "the `telemetry` section": it is not an exporter)

## Checking calls against the vendor's spec

**Spec**:
Our copy of a vendor's OpenAPI description, which the proxy reads to find a call's route, check it and know what to hide.
_Avoid_: schema (that is one part of a spec), contract, definition

**Base path**:
The path part of a spec's server address, such as `/v1` in `https://api.x.com/v1`, put in front of every route. The host part is never used, as the config names the hosts.
_Avoid_: server path, prefix, root

**Mark**:
A field of our own in a spec, whose name starts with `x-hotline-`, for what plain OpenAPI cannot say. Marks live only in specs and overlays, never in the config.
_Avoid_: annotation, extension, tag

**Overlay**:
A small file that changes a spec as it is loaded, so one api can add its own details (a queue, a message schema) to a shared vendor spec.
_Avoid_: patch, override

**Route**:
The operation in the spec that a call matches, shown in the record as its URL template, such as `/v1/customers/{customer}`.
_Avoid_: endpoint, path (the path is the call's real address)

**Action**:
For vendors that send every call to the same path, such as AWS's older APIs, the name of what the call does, read from a header or body field and used to pick the route.
_Avoid_: operation, command

**Spec check**:
Checking one side of a call, the request or the response, against the spec. It runs beside the call on a worker and never holds it up. The `checks` list on an api says which parts are checked: path, query, headers, cookies, body, status, response headers, response body, and a WebSocket's messages.
_Avoid_: validation (used for the config, by `hotline-proxy validate`), verification (used for the ledger), schema check

**Webhook address**:
An address a vendor gives an app to post messages to, such as a Slack incoming webhook, `https://hooks.slack.com/services/T000/B000/XXXX`. The address is itself the secret (see decision 39 in `docs/decisions/spec-checks.md`).
_Avoid_: hook URL, webhook (alone, it also means the vendor calling the app)

**Body copy**:
The copy of a request or response body that the proxy keeps, up to a size limit, for spec checks, while the body itself streams on untouched.
_Avoid_: buffer, captured body

**Message schema**:
One kind of WebSocket message, written as in AsyncAPI 3 in the spec's `components/x-async-api/messages`, and listed by an operation under the `x-async-api` mark on the operation that answers `101`. Its payload is a schema in `components/schemas`, whose `x-hotline-match` fields name a message.
_Avoid_: AsyncAPI document (not used), channel, message type

**Message operation**:
One exchange of a WebSocket as the spec's `x-async-api` lists it, such as `toolCall`: the messages one side sends and, in its `reply`, those the other side sends back. Each message schema belongs to exactly one. Not the route's OpenAPI operation, which is the call that opens the WebSocket.
_Avoid_: exchange, pair

**Message role**:
What a message is within its operation, found from the spec: `request` (its operation has a reply), `reply` (it is in a reply), `answer` (it has a correlation id but no operation's reply holds it) or `push` (tied to no request).
_Avoid_: message type, kind

**Correlation id**:
The one field of a WebSocket message, named by its `correlationId`, that a request and its replies share, such as Kraken's `req_id`. Recorded as `messaging.message.conversation_id`. The proxy only writes it down; it pairs nothing.
_Avoid_: pair id, id tuple

**Spec check line**:
Where spec checks wait when every worker is busy. Its memory is capped by `specChecks.queueMemory`; past the cap the oldest waiting checks are skipped, never the call.
_Avoid_: backlog, check queue

**Branch note**:
What a spec's discriminator says, handed to the schema check as plain JSON under `x-hotline-branch`: the field that tells the branches of a oneOf or anyOf apart, and which branch each of its values means. It only chooses which problems are reported when the choice fails, never whether a call passes.
_Avoid_: discriminator (the OpenAPI word, used only in spec)

## What the proxy writes down

**Record**:
The one entry the proxy writes per call, with everything known about it, sent as an OpenTelemetry log record named `hotline.request`. The proxy judges nothing; a record only says what happened.
_Avoid_: request record, call record (in prose; fine as an attribute name), log line, event, verdict

**Full record**:
The record of a call whose integration ID is known and whose host is in that integration's apis: with route, checks and redacted details.
_Avoid_: rich record, detailed record

**Minimal record**:
The record of every other call: not-passed calls, hosts outside the integration, and calls the proxy refused. It has body sizes but no checks and no route.
_Avoid_: short record, partial record, empty record

**Session record**:
The record of a WebSocket session, written when it ends: bytes each way, how long it was open, and, when its messages are read, how it ended and its pings and pongs. It is not counted as a call.
_Avoid_: second record

**Message**:
One WebSocket message after the `101`, which may come in several frames (a frame is one piece of a message, with a short header). Pings, pongs and close messages are control frames, not messages.
_Avoid_: frame (that is a piece of a message), event, packet

**Message record**:
The record of one message of a WebSocket session, `hotline.websocket.message`, with its index, direction, name, sizes, ids and spec check. It is not counted as a call.
_Avoid_: message event, frame record

**Streamed response**:
A response sent as a series of stream items while the vendor works, such as Server-Sent Events, JSON Lines or a JSON sequence, whose spec gives its media type an `itemSchema`. The call ends when its headers reach the app; what follows is the stream.
_Avoid_: streamed reply, chunked response

**Stream item**:
One piece of a streamed response, checked on its own against the `itemSchema`: for Server-Sent Events, one event with data, ended by a blank line; for JSON Lines, one line that is not empty; for a JSON sequence, one record, started by the record separator (byte 0x1E).
_Avoid_: item on its own (too general), chunk (that is how the bytes happened to arrive), message (that is a WebSocket's)

**Stream record**:
The record of a streamed response, `hotline.stream`, written when it ends: its stream items, how many broke the spec, its bytes and their hash, how long it was open, and whether it ended whole or was cut. It is not counted as a call.
_Avoid_: second record

**Stream item record**:
The record of one stream item of a streamed response, `hotline.stream.item`, with its index, size and spec check. It is not counted as a call.
_Avoid_: event record, chunk record

**Probe**:
A check of a vendor's host and port after calls to it fail to connect: DNS, the path trace (when the port gives no answer), the TCP connect and TLS, to say which side of the connection the problem is on. One record (`hotline.probe`) and one trace each, shared by every integration calling that host and port, and backing off while the failures go on.
_Avoid_: diagnosis, health check (that is `/livez` and `/readyz`), connection check (a check is a spec check)

**Path trace**:
The part of a probe that sends packets which may cross only 1 router, then 2, and so on, over TCP and UDP, to see how far the packets get towards the vendor, as `traceroute` does. Each packet is a `hop` span.
_Avoid_: traceroute (the tool), path probe (the whole probe is the probe)

**Trace**:
A call's steps as OpenTelemetry spans (DNS, connect, TLS, first byte and the rest), each with its start and end. The timings live here.
_Avoid_: timings record, span list

**Level**:
The severity on each record (INFO, WARN, ERROR), so a log tool can show which calls went wrong.
_Avoid_: log level (that is the proxy's own log, set with `--log-level`), severity (in prose)

**Redaction**:
Hiding a value from a record by writing `REDACTED` in its place. Path and query values are hidden unless the spec marks them safe; login headers are always hidden.
_Avoid_: masking, scrubbing, anonymising

**Body field**:
One value from inside a body that the spec marks with `x-hotline-record`, copied into the record because it is safe and helps find a call, such as an order number.
_Avoid_: extracted field, tag

**Full content**:
With `content: full` on an integration, file exporters get every header and the bodies of its calls, with the values the spec marks secret, and logins, still redacted. Never sent over OTLP.
_Avoid_: debug mode, raw dump, full record (that is a different thing)

**Exporter**:
One place that records and traces are sent to, listed under `exporters`: `otlp`, `file` or `console`. Each has its own queue, so a slow one never holds up the calls or the others.
_Avoid_: sink (the group of all exporters is `export.Exporters`), destination (in prose and in code names, such as `fileDestination`), output, backend, collector (that is the server an `otlp` exporter sends to)

## Evidence ledger

**Evidence ledger**:
The proxy's way of making records provable: each record carries a hash (a short value that changes when any byte of the record changes), and every few seconds the hashes of each integration's records go into a signed batch that points to the batch before.
_Avoid_: signatures (the old signed records, replaced), audit log; for the hash: digest, checksum, fingerprint (Hotline's later Witness service uses that word for a batch)

**Batch**:
One integration's record hashes from one time window, put in a tree whose top is signed by the proxy and which holds the top of the batch before, so a missing batch shows.
_Avoid_: block, bundle, chunk

**Chain**:
The line of batches of one integration, each pointing to the one before.
_Avoid_: log, sequence

**Verify**:
`hotline-proxy verify`: checking a folder of records against their hashes, chains and signatures, with no secret, config or network.
_Avoid_: validate (that checks a config), audit, check (that is a spec check)

## Building and testing

**Slice**:
A small piece of the proxy built end to end, tests first.
_Avoid_: milestone, phase, sprint

**System under test**:
The whole proxy program as tests drive it, through its ingresses, with fake vendors and a fake collector at its edges.
_Avoid_: unit, component under test

**Fake vendor**:
A small server in the tests that plays a vendor, so tests never call a real one.
_Avoid_: mock, stub
