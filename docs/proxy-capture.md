# The capture ingress

For programs on a Mac that cannot be told about a proxy, and that you want to watch one by one. A small macOS helper (a system extension, built in its own project, `petercipov/hotline-macos-capture`) picks out the connections of the apps named by the `--capture` flag and hands each one to the proxy as a CONNECT on a listener with the `capture` part, a socket file (a file on disk that programs on this Mac connect to instead of a network port). Other apps on the Mac are untouched. The proxy never starts the apps itself (Peter, 2026-11-10). This page is the Go side. The `--capture` flag, which unpacks and starts the helper with the app list, comes later and needs a Mac.

## What the extension sends

For each captured connection, a standard CONNECT, sent over the socket file, to where the app really called (often an address), with a User-Agent and three headers of ours:

```
CONNECT 93.184.215.14:443 HTTP/1.1
Host: 93.184.215.14:443
User-Agent: HotlineCapture/0.1.4
Hotline-Capture: 2
Hotline-Capture-Pid: 4242
Hotline-Capture-Program: /usr/bin/curl
```

`Hotline-Capture` is the version, now 2, which goes over the socket file. Version 1, sent to the proxy port by extension 0.1.x, is refused like any unknown version. The User-Agent names the extension's release; the proxy does not record it. The full contract is `docs/protocol.md` of `hotline-macos-capture`; change both together. The app list never reaches the proxy: Hotline gives it to the helper at start.

## How the proxy handles it

- The capture listener has its own cert and passthrough list, so it needs no `connect` part. With only a capture listener, Hotline opens no port at all and takes captured connections only (Peter, 2026-11-12).
- The capture headers count only on the capture listener. On the proxy port they are ignored, and the CONNECT is an ordinary one, so no other program and no other machine can name a program (Peter, 2026-11-12).
- Only the proxy's own user and root can open the socket file, and the extension runs as root, so the headers there are believed as they come (Peter, 2026-11-12). A request other than CONNECT gets `405`.
- An unknown version, a header given twice, a process number that is not a positive number, or a program path over 4096 bytes is refused with `400` and recorded with `hotline.ingress=capture`.
- After the `200`, the tunnel is served like an intercepted connection (from step 2 of [proxy-intercept.md](proxy-intercept.md)): the site comes from the TLS handshake, with the CONNECT's host when the handshake names none; the port is the CONNECT's port; a site in the capture `passthrough` is relayed untouched; ECH is rejected so the program retries without it.
- The extension hands over every connection of a captured program, on any port, so `capture.ports` names the ports read as calls, 80 and 443 when left out (Peter, 2026-11-13), like a mode in HAProxy. On a read port, a TLS handshake is served as above, and plain HTTP is served like any other call, as in an ordinary tunnel, unless the CONNECT's host is in `passthrough`; bytes that are neither are closed and recorded with `error.type=not_http`, so a wrong port list shows. Every other port is tcp: relayed untouched at once, with `hotline.intercept.reason=tcp`, so a server that speaks first, such as SSH, just works. A program that closes before sending anything makes no record, like the extension's own check.
- Records say `hotline.ingress=capture`, `process.pid` and `process.executable.path`: the call's record and the minimal records of failed or relayed connections. The integration ID comes from `Proxy-Authorization`, as for any CONNECT; the extension sends none yet, so it is `not-passed`.
- The extension checks every 2 seconds that the proxy is there by opening a connection and closing it, sending nothing. That makes no record.

## The socket file

```yaml
certs:
  capture: { cert: ./ca/cert.pem, key: ./ca/key.pem }             # a certificate authority
listeners:
  mac-calls:
    address: "unix://~/Library/Application Support/Hotline/capture.sock"   # the default Hotline uses on a Mac
    capture:
      cert: capture                                                # signs the certificates for the sites
      passthrough: [pinned.example.com]                            # relayed untouched, empty by default
      ports: [80, 443]                                             # ports read as calls, the default; every other port is relayed untouched
exporters:
  - console: {}
```

A config may have only this listener. A reload that takes the listener out keeps the socket file open, as it started, until a restart.

- The path is absolute or starts with `~/` (the home folder, from `HOME`), and is at most 103 bytes, the longest a socket file may have on macOS.
- The proxy makes the folder if it is missing (owner only), and gives the file to its owner only. That mode is what keeps other users' programs out; a mode that cannot be set stops the proxy from starting.
- A socket file left by an earlier run is replaced. A path another program listens on, or a plain file at the path, stops the proxy from starting; a plain file is never removed.
- The file is removed when the proxy stops. Adding, removing or moving the listener needs a restart.

## Passthrough on connect

```yaml
listeners:
  apps:
    address: tcp://:8080
    connect:
      cert: customer
      passthrough: [pinned.example.com, "*.internal.example.com"]   # relayed untouched, empty by default
```

Same meaning as for `transparent`, for ordinary tunnels, matched by the host in their CONNECT. Captured tunnels use the list of the capture part, matched by the site name in the handshake. Relayed tunnels are dialed by name and recorded with `hotline.intercept.mode=passthrough`.

A relayed tunnel stops each direction on its own: when the app or the vendor stops sending, the proxy tells the other side that nothing more comes, and keeps the other direction open. This holds for ordinary tunnels over HTTP/1.1 and for captured tunnels. Over HTTP/2 only the app can stop first and still get the whole reply: the reply of an HTTP/2 stream ends only with the whole stream, so when the vendor stops first the tunnel ends (`features/half-close.feature`).

Decisions: [docs/decisions/capture.md](decisions/capture.md).
