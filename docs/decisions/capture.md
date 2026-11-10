# Capture ingress decisions (Peter, 2026-11-10)

Full wording in [docs/proxy-capture.md](../proxy-capture.md). The macOS side lives in `petercipov/hotline-macos-capture`.

1. Hotline never runs the programs it captures; it only watches for the apps named in `listen.capture.apps`.
2. A separate `listen.capture` block, not an extra field of `listen.intercept`. Its fields: `issuer`, `integration`, `apps`, `passthrough`, `ech.relay`. No address: the proxy makes the socket.
3. The socket is in a private folder: random name, mode 0700, owned by the user. At start the proxy checks owner and mode and refuses to start otherwise. No check of who connects.
4. Messages: our own small versioned JSON format with a 4 byte size in front (limit 16 KiB), kept in step with `docs/protocol.md` of `hotline-macos-capture`. Version 1.
5. New contracts, approved by Peter: `config.Capture` with `Config.Capture`, and `Proxy.CaptureSocket() string`. No change to `proxy.Options`.
6. Records: `hotline.ingress=capture`, `process.pid`, `process.executable.path`, and `error.type=capture_bad_hello` for a first message that cannot be used.
7. Slice B (the `--capture` flag, unpacking and starting the signed helper app, embedding it in the macOS binary) is later and needs a Mac.
