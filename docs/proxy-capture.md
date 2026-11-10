# The capture ingress

For programs on a Mac that cannot be told about a proxy, and that you want to watch one by one. A small macOS helper (a system extension, built in its own project, `petercipov/hotline-macos-capture`) picks out the connections of the apps named by the `--capture` flag and hands each one to the proxy port as a CONNECT. Other apps on the Mac are untouched. The proxy never starts the apps itself (Peter, 2026-11-10). This page is the Go side. The `--capture` flag, which unpacks and starts the helper with the app list, comes later and needs a Mac.

## What the extension sends

For each captured connection, a standard CONNECT to where the app really called (often an address), with a User-Agent and three headers of ours:

```
CONNECT 93.184.215.14:443 HTTP/1.1
Host: 93.184.215.14:443
User-Agent: HotlineCapture/0.1.4
Hotline-Capture: 1
Hotline-Capture-Pid: 4242
Hotline-Capture-Program: /usr/bin/curl
```

`Hotline-Capture` is the version, now 1. The User-Agent names the extension's release; the proxy does not record it. The full contract is `docs/protocol.md` of `hotline-macos-capture`; change both together. The app list never reaches the proxy: Hotline gives it to the helper at start.

## How the proxy handles it

- It needs `listen.connect`, the CONNECT ingress, and uses its issuer.
- The capture headers count only on a CONNECT from a loopback address. From anywhere else they are ignored, and the CONNECT is an ordinary one, so no other machine can name a program.
- An unknown version, a header given twice, a process number that is not a positive number, or a program path over 4096 bytes is refused with `400` and recorded with `hotline.ingress=capture`.
- After the `200`, the tunnel is served like an intercepted connection (from step 2 of [proxy-intercept.md](proxy-intercept.md)): the site comes from the TLS handshake, with the CONNECT's host when the handshake names none; the port is the CONNECT's port; a site in `listen.connect` `passthrough` is relayed untouched; ECH is rejected so the program retries without it.
- Records say `hotline.ingress=capture`, `process.pid` and `process.executable.path`: the call's record and the minimal records of failed or relayed connections. The integration ID comes from `Proxy-Authorization`, as for any CONNECT; the extension sends none yet, so it is `not-passed`.
- The extension checks every 2 seconds that the proxy is there by opening a connection and closing it, sending nothing. That makes no record.

## Passthrough on listen.connect

```yaml
listen:
  connect:
    issuer: customer
    passthrough: [pinned.example.com, "*.internal.example.com"]   # relayed untouched, empty by default
```

Same meaning as for `listen.intercept`. A captured tunnel is matched by the site name in the handshake, an ordinary tunnel by the host in its CONNECT. Relayed tunnels are dialed by name and recorded with `hotline.intercept.mode=passthrough`.

Decisions: [docs/decisions/capture.md](decisions/capture.md).
