# Capture ingress decisions (Peter, 2026-11-10)

Full wording in [docs/proxy-capture.md](../proxy-capture.md). The macOS side lives in `petercipov/hotline-macos-capture`; the plan is `mitmproxy-ideas/capture-connect-plan.md` in the project files.

1. Hotline never runs the programs it captures; it only watches for the apps named by `--capture`.
2. Each captured connection is a standard CONNECT to the proxy port with the headers `Hotline-Capture` (version, now 1), `Hotline-Capture-Pid` and `Hotline-Capture-Program`, plus a standard `User-Agent: HotlineCapture/<version>`. This replaces the earlier framed JSON messages, the control connection and the use of `listen.intercept`.
3. Captured tunnels are served by the existing CONNECT ingress (`listen.connect`) and handled like intercepted connections inside, with `hotline.ingress=capture`.
4. The app list is not sent to the proxy. Hotline passes it to the helper at start; a new list means a new start. Nothing new in `proxy.Options`.
5. ~~The capture headers count only from a loopback address.~~ Replaced on 2026-11-12, see 11.
6. `listen.connect` gets a `passthrough` list, same meaning as for intercept, for captured and ordinary tunnels. Contract: `config.Passthrough` and `Config.ConnectPassthrough`.
7. Records: `hotline.ingress=capture`, `process.pid`, `process.executable.path`. The extension's User-Agent is not recorded (Peter).
8. The extension checks every 2 seconds that the proxy answers and captures nothing while it does not. An empty connection makes no record.
9. One Hotline captures all chosen programs.
10. Later: the `--capture` flag (unpacking and starting the signed helper app, embedded in the macOS binary), which needs a Mac.
11. (Peter, 2026-11-12) The extension sends its CONNECT over `listen.capture`, a socket file, not the proxy port: any program on the Mac could reach a loopback port and name a program. A shared secret was turned down, as it can leak. The capture headers on the proxy port are ignored from now on, which breaks extension 0.1.x. Only the proxy's user and root can open the socket file (mode 0600), and the extension runs as root, so the proxy believes the headers there without asking who is on the other end; a check for root was built and then dropped as not needed (Peter). Hotline's default path is `~/Library/Application Support/Hotline/capture.sock`. Capture version 2 of the macOS side goes with it: the proxy takes only version 2 and refuses version 1 with `400`.
12. (Peter, 2026-11-12) `listen.capture` is an object with its own `path`, `issuer` and `passthrough`, no longer borrowing `listen.connect`, and `listen.proxy` may be left out when `listen.capture` or `listen.intercept` is set, so Hotline on a Mac can take captured connections with no port open. This replaces point 3's use of `listen.connect` for captured tunnels.
13. (Peter, 2026-11-13) A captured tunnel is not always TLS, since the extension hands over every connection of a captured program. Plain HTTP is served like any other call; anything else, and a program silent for one second (the server speaks first), is relayed untouched with `hotline.intercept.reason` `not_http` or `app_silent`.
