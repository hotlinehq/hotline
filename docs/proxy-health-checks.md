# Health checks on the admin port

Agreed with Peter on 2026-10-31. Tests: `src/hotline-proxy/features/health-checks.feature` (end to end, against the built program) and the admin rows in `features/config.feature`.

## What it does

Kubernetes asks each copy of the proxy two questions over plain HTTP, on the admin port (`listen.admin`), never on the proxy port:

- `GET /livez`: "is the process alive?" If the answer is no, Kubernetes restarts the proxy.
- `GET /readyz`: "should I send you calls?" If the answer is no, Kubernetes stops sending new calls to this copy but leaves it running.

Yes is `200`, no is `503`. `HEAD` works too. Any other path gets `404`, any other method `405`. Health check calls make no records and no counts. The answer is only a yes or a no: the state of each part comes with the proxy's own telemetry: [proxy-telemetry.md](proxy-telemetry.md).

## Decisions (Peter, 2026-10-31)

1. `/readyz` says no only before the first config and all its specs have loaded, while the proxy port is not yet listening, and from the first stop signal on. The admin port opens only after the config is loaded and the proxy port listens, so until then Kubernetes can't reach it, which counts as no.
2. A failed config reload will never make `/readyz` say no; the proxy keeps the last good config. Every copy usually reads the same file, so one bad edit would otherwise take every copy out of service at once. (Reload itself is a later chapter.)
3. A bad config at start stops the proxy (exit code 1), so a rollout with a bad config halts while the old copies keep serving.
4. A destination that is down never makes `/readyz` say no.
5. On the stop signal `/readyz` says no at once, and calls in progress finish.
6. `/livez` says no only when the proxy is stuck: if the admin port answers, the proxy is alive.
7. After the stop signal the proxy keeps taking new calls for `--drain-delay` (5 seconds unless set, part of the 25 second `--shutdown-timeout`), then closes the proxy port. Kubernetes needs a few seconds to stop sending calls to a copy, and our image has no shell for the usual `sleep` before stopping. A second signal skips the wait; `--drain-delay 0s` turns it off.
8. Without `listen.admin` there are no health checks, and the proxy logs one warning at start, so it still runs outside Kubernetes.
9. Plain HTTP only on the admin port. It must never be in a Kubernetes Service or Ingress; only the kubelet (the Kubernetes agent on each machine) calls it. The health checks answer until the very end of a stop.

## Kubernetes probe settings

```yaml
containers:
  - name: hotline-proxy
    args: ["run", "--format", "json", "--config", "/etc/hotline/hotline-proxy.yaml"]
    ports:
      - { name: proxy, containerPort: 8080 }
    livenessProbe:
      httpGet: { path: /livez, port: 8081 }
      periodSeconds: 10
    readinessProbe:
      httpGet: { path: /readyz, port: 8081 }
      periodSeconds: 2
terminationGracePeriodSeconds: 30
```

The same probes work for a sidecar. The admin port (`8081` here) is left out of the Service.
