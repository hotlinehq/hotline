# Capture ingress decisions (Peter, 2026-11-10)

Full wording in [docs/proxy-capture.md](../proxy-capture.md). The macOS side lives in `petercipov/hotline-macos-capture`.

1. Hotline never runs the programs it captures; it only watches for the apps named by `--capture`.
2. One listener: capture reuses `listen.intercept` (its port, issuer, integration, `passthrough`, `ech.relay`). No second listener, no socket of its own, no private folder.
3. The app list is a parameter of `--capture`, not in the config file. In Go: `proxy.Options.CaptureApps []string`.
4. The port tells the extension's messages from TLS by the first byte (0x00 or 0x16). Without `CaptureApps` the messages are refused as bytes that are not TLS.
5. Safety: when capturing, `listen.intercept` must be a loopback address, or the proxy refuses to start. There is no check of who connects.
6. Messages: our own small versioned JSON format with a 4 byte size in front (limit 16 KiB), kept in step with `docs/protocol.md` of `hotline-macos-capture`. Version 1.
7. Contract: new `Options.CaptureApps`. No `config.Capture`, no `Proxy.CaptureSocket()`, nothing new in the config.
8. Records: `hotline.ingress=capture`, `process.pid`, `process.executable.path`, and `error.type=capture_bad_hello` for a first message that cannot be used.
9. One Hotline captures all chosen programs. Several Hotlines for several programs are not needed (Peter).
10. Slice B (the `--capture` flag, unpacking and starting the signed helper app, embedding it in the macOS binary) is later and needs a Mac.
