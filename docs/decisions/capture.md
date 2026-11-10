# Capture ingress decisions (Peter, 2026-11-10)

Full wording in [docs/proxy-capture.md](../proxy-capture.md). The macOS side lives in `petercipov/hotline-macos-capture`; the plan is `mitmproxy-ideas/capture-connect-plan.md` in the project files.

1. Hotline never runs the programs it captures; it only watches for the apps named by `--capture`.
2. Each captured connection is a standard CONNECT to the proxy port with the headers `Hotline-Capture` (version, now 1), `Hotline-Capture-Pid` and `Hotline-Capture-Program`, plus a standard `User-Agent: HotlineCapture/<version>`. This replaces the earlier framed JSON messages, the control connection and the use of `listen.intercept`.
3. Captured tunnels are served by the existing CONNECT ingress (`listen.connect`) and handled like intercepted connections inside, with `hotline.ingress=capture`.
4. The app list is not sent to the proxy. Hotline passes it to the helper at start; a new list means a new start. Nothing new in `proxy.Options`.
5. The capture headers count only from a loopback address; from anywhere else they are ignored. `listen.proxy` may be any address.
6. `listen.connect` gets a `passthrough` list, same meaning as for intercept, for captured and ordinary tunnels. Contract: `config.Passthrough` and `Config.ConnectPassthrough`.
7. Records: `hotline.ingress=capture`, `process.pid`, `process.executable.path`, and `user_agent.original` with the extension's User-Agent.
8. The extension checks every 2 seconds that the proxy answers and captures nothing while it does not. An empty connection makes no record.
9. One Hotline captures all chosen programs.
10. Later: the `--capture` flag (unpacking and starting the signed helper app, embedded in the macOS binary), which needs a Mac.
