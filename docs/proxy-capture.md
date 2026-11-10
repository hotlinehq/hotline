# The capture ingress

For programs on a Mac that cannot be told about a proxy, and that you want to watch one by one. A small macOS helper (a system extension, built in its own project, `petercipov/hotline-macos-capture`) picks out the secure calls of the apps named by the `--capture` flag and hands them to the proxy's `listen.intercept` port. Other apps on the Mac are untouched. The proxy never starts the apps itself (Peter, 2026-11-10). This page is the Go side, slice A. The `--capture` flag that fills `Options.CaptureApps`, and unpacks and starts the helper, is slice B, later, and needs a Mac.

## No config of its own

Capture reuses the intercept ingress: the same port, issuer, integration, `passthrough` and `ech.relay` (see [proxy-intercept.md](proxy-intercept.md)). The only new input is `proxy.Options.CaptureApps`, the programs to capture, by name or process number, a leading `!` skipping the match (Peter, 2026-11-10: it is a parameter of `--capture`, not in the config file). With no apps the proxy behaves exactly as before, and the extension's messages are refused like any bytes that are not TLS.

The proxy refuses to start when it is asked to capture and:

- an app is not a name or a process number (`!` alone, a space in a name);
- the config has no `listen.intercept`;
- `listen.intercept.address` is not a loopback address (`127.0.0.1`, `::1`, `localhost`), so no other machine can claim to be a program.

## Messages

A TLS handshake starts with the byte 0x16. The extension's messages start with a 4 byte size, at most 16 KiB, so with the byte 0x00. The port tells them apart by the first byte. Each message is that size, then JSON with a `version` (now 1). The exact messages are in `docs/protocol.md` of `hotline-macos-capture`; change both together. A message that is too large, not JSON, of another version or a flow with no usable destination closes the connection and is recorded with `error.type=capture_bad_hello`.

- **Control** (`role: control`, one, kept open): the proxy answers at once with the app list, `{"version":1,"entries":[...]}`. The list always ends with a skip rule for the proxy's own process number, so its calls to vendors are never captured again. The list does not change while the proxy runs. With no control connection nothing is captured, and that is not an error.
- **Flow** (`role: flow`, one per captured call): `host` and `port` are where the program really called, `pid` and `processName` which program. After the hello come the program's own bytes, which are the TLS handshake.

## How a flow is handled

The same as an intercepted connection, from step 2 of [proxy-intercept.md](proxy-intercept.md), with these differences: the port is the hello's port; with no site name in the handshake the host is the hello's host (a name or an address); and the records say `hotline.ingress=capture` plus `process.pid` and `process.executable.path` from the hello (the call's record and the minimal records of failed or relayed connections).

Decisions: [docs/decisions/capture.md](decisions/capture.md).
