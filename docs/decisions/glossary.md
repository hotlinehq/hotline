# Glossary decisions (Peter, 2026-11-19)

The words themselves live in [GLOSSARY.md](../../GLOSSARY.md). These are the choices behind them, made one question at a time with decomplect's Hammock mode.

1. The glossary is `GLOSSARY.md` at the root of the repository, beside `CLAUDE.md` and `memory.md`. Revisit if the metrics module or the examples get words of their own.
2. The problem it solves: renames reached the config but not the docs, code and tests, so a reader could not tell whether two words name one thing or two.
3. Once a word is settled, the old word is renamed everywhere at once, in one sweep per word: docs, Go code, log and span text, feature files and their steps. Revisit if a sweep grows too large to review in one pull request.
4. "Certificate authority" replaces "issuer" in both its meanings: the proxy's own signing pair (a `certs` entry used with `mode: sign`) and the authority that signed a vendor's certificate. In Go names it is `authority`. "Cert" stays the name of the config entry. The old config key `issuers` keeps its name where its warning and the old-layout docs mention it, and cert-manager's own `Issuer` kind is not ours to rename.
5. `config.Intercept` and `interceptFile` become `Transparent` and `transparentFile`, and the files and tests named after the old intercept ingress follow. "Intercept" means only interception, the proxy posing as the vendor, so `interceptor` and the `hotline.intercept.*` record attributes keep their names.
6. "Response" everywhere for the vendor's answer, in prose, Go names, log and span text and tests, as HTTP, Go, OpenTelemetry and OpenAPI say it. "Reply" is not used.
7. The `checks` list takes `response-headers` and `response-body`. The old `reply-headers` and `reply-body` are refused at start, like any unknown check.
8. Each entry under `exporters` is an exporter, never a destination, in docs and code (`fileDestination` became `fileExporter`). Where the proxy's own numbers go is the `telemetry` section, which is not an exporter. The network sense of destination (`OriginalDestination`, where a redirected connection was first sent) and OpenTelemetry's `messaging.destination.name` stay.
9. `export.Sink`, the part that hands each call to every exporter, is `export.Exporters`, as the architecture diagram calls it.

Standing rule: breaking changes to the config and the records are allowed (Peter, 2026-11-19).

## Tripwires (2026-11-25)

- Confirmation: `GLOSSARY.md` is at the root of the repository, and no package declares `export.Sink` or `config.Intercept`.
- Revisit if: see the Revisit if lines in the decisions above.
