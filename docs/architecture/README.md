# Architecture

Two pictures of what is built today, drawn from the code and these docs. When a pull request adds, removes or renames a part of the proxy, an ingress, an exporter or something outside it talks to, it updates both pictures too.

- [hotline-proxy.svg](hotline-proxy.svg): the proxy at a glance, with [hotline-proxy.png](hotline-proxy.png) made from it for the main README. A hand-written SVG: the six ingresses, the call path to the vendor past the address guard (the check that refuses loopback and private addresses), the checks and records beside the call, and where records go. The disk queue an otlp exporter keeps while its collector is down is drawn inside the exporters, as it is part of them, not an output.
- [hotline.c4](hotline.c4): the same parts as a C4 model in [LikeC4](https://likec4.dev) format. C4 is a way to draw software at a few zoom levels. Its views are `index` (the system and what it talks to), `containers` (the programs and stores inside Hotline) and `proxy` (the parts of `hotline-proxy run`).

To see the C4 views, from the root of the repo:

```
pkill -f likec4
npx likec4@1.60.0 start docs/architecture
```

Then open http://localhost:5173/view/index. Check the model after a change with `npx likec4@1.60.0 validate docs/architecture`.
