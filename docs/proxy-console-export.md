# Printing every call to the console

A console exporter prints each call to the proxy's standard output as two short lines, like `mitmdump` (mitmproxy's command line tool): the request, then the reply under it. An invalid call gets a third line with what the checks found. It is for watching calls go by on a laptop, without a collector. All of it was agreed with Peter on 2026-11-11; the behaviour is written down in `src/hotline-proxy/features/console-export.feature`.

## Config

```yaml
exporters:
  - console: {}            # no settings needed
  # - console:
  #     color: auto        # auto (default), always or never
```

A console exporter may stand alone or next to `otlp` and `file` exporters. There is one at most. Changing it needs a restart.

## What it prints

```
16:29:15.473 checkout: POST https://api.shop.test/orders
                    << 201 Created 0 B 2.6 ms valid
16:29:15.512 checkout: GET https://api.shop.test/orders?limit=abc
                    << 200 OK 14 B 3.9 ms invalid
                       request: query limit type integer
16:29:16.020 checkout: GET https://api.shop.test/orders
                    << 502 Bad Gateway connection_refused 1.1 ms
```

- The request line: the call's time, the integration, the method and the address as the app sent it, without a user or password.
- The reply line: `<<` ends just before the method. Then the status and its name, the proxy's failure when there is one (the record's `error.type`), the reply's size, the total time, and the check status when the call has a spec.
- The findings line, for an invalid call: what the checks found on each side, written "part place rule expected", never a value from the call.
- No headers, no bodies, no spans, no metrics. `content: full` never reaches the console.

A long address is cut, as mitmdump does, to the terminal's width less 25 characters, and at least 50, ending in `…`. The width is read from the terminal, else from the `COLUMNS` variable, else it is 80.

## Colour

With `color: auto`, the lines are coloured when standard output is a terminal and `NO_COLOR` is not set; otherwise they are plain text. `always` and `never` force it.

| What | Colour |
| --- | --- |
| time | dim |
| integration | cyan |
| method | bold: GET green, DELETE red, others magenta |
| address | bold |
| status | bold: 2xx green, 3xx magenta, 4xx yellow, 5xx and failures red |
| failure | bold red |
| total time | yellow past one second |
| check status | valid green, invalid red, skipped dim |
| findings | dim, with the side in red |

## The proxy's own logs

With a console exporter in the config, the proxy's own logs are plain lines too, not JSON (Peter, 2026-11-12). They stay on standard error, so `2>/dev/null` hides them, and they are coloured by the same `color` setting, judged on standard error:

```
07:48:03.662 WARN  config warning: there is no listen.admin address, so the proxy has no health checks
07:48:03.662 INFO  hotline proxy started  address=127.0.0.1:8080 config_version=hotline-proxy.yaml+5692399ece3d
```

Each line has the time, the level (WARN yellow, ERROR red, DEBUG dim), the message, a reason after a colon, and the other fields as `name=value` with dim names. The config is read before the first line, so its warnings are plain too; a config the proxy cannot load is still logged as JSON, as is `hotline-proxy validate`. Without a console exporter the logs stay JSON, for Kubernetes and log collectors.

## Safety and speed

- Every value that came with a call is escaped before it is printed: control characters as `\u009b`, bytes that are not UTF-8 as `\xff`. A crafted address cannot move the cursor or change the terminal's colours.
- The proxy's own JSON logs stay on standard error, so the two never mix.
- A call's lines are printed together, at once, from a queue of at most 1,000 calls. When the terminal cannot keep up, the newest calls are dropped and counted, as with the other exporters, and a failed write is warned about. A call never waits for the console.
- The program reads the terminal's width with `golang.org/x/sys`, which the proxy already used; no new module.
