# The capture ingress

For programs on a Mac that cannot be told about a proxy, and that you want to watch one by one. A small macOS helper (a system extension, built in its own project, `petercipov/hotline-macos-capture`) picks out the secure calls of the apps named in `listen.capture.apps` and hands them to the proxy over a local socket (a file-like channel). Other apps on the Mac are untouched. The proxy never starts the apps itself (Peter, 2026-11-10). This page is the Go side, slice A: the socket and everything behind it. The `--capture` flag that unpacks and starts the helper is slice B, later, and needs a Mac.

## Config

```yaml
listen:
  capture:
    issuer: customer               # a name from the issuers section
    integration: ci-lint           # the ID of every captured call; not-passed if left out
    apps: [curl, firefox, "!1234"] # names or process numbers; a leading ! skips the match
    passthrough: [pinned.example.com]   # relayed untouched, as for intercept
    ech: { relay: [outer.example.net] }
```

There is no address: the proxy makes the socket itself, in a private folder (random name, mode 0700, owned by the user, in the user's temporary folder). At start the proxy checks that the folder has exactly that owner and mode, and refuses to start otherwise. It does not check who connects: the private folder keeps other users out, and programs of the same user are trusted (Peter, 2026-11-10). The folder and socket are removed when the proxy stops. `Proxy.CaptureSocket()` gives the path.

`issuer` must be a name from `issuers`, and `apps` must not be empty (a list with only skip rules would capture everything else). Adding or removing `listen.capture` needs a restart. A renewed issuer and a changed `apps` list are picked up by the config reload.

## Messages

Both connections from the extension start with one framed hello message: 4 bytes with the size of the body (big endian, at most 16 KiB), then JSON with a `version` (now 1). The exact messages are in `docs/protocol.md` of `hotline-macos-capture`; change both together. A message that is too large, not JSON, of another version or a flow with no usable destination closes the connection and is recorded with `error.type=capture_bad_hello`.

- **Control** (`role: control`, one, kept open): the proxy answers at once with the app list, `{"version":1,"entries":[...]}`, and again after a reload that changes `apps`. The list always ends with a skip rule for the proxy's own process number, so its calls to vendors are never captured again. With no control connection nothing is captured, and that is not an error.
- **Flow** (`role: flow`, one per captured call): `host` and `port` are where the program really called, `pid` and `processName` which program. After the hello come the program's own bytes, which are the TLS handshake.

## How a flow is handled

The same as the intercept ingress, from step 2 of [proxy-intercept.md](proxy-intercept.md), with these differences: the port is the hello's port; with no site name in the handshake the host is the hello's host (a name or an address); and the records say `hotline.ingress=capture` plus `process.pid` and `process.executable.path` from the hello (the call's record and the minimal records of failed or relayed connections). The client address of a captured call is `127.0.0.1`, since the program is on this machine.

Decisions: [docs/decisions/capture.md](decisions/capture.md).
