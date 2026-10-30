# Shadow Failure-Specific Evidence Progress

- [x] Inspect current shadow attempt, clone, and analyzer evidence paths.
- [x] Record the evidence scope and non-goals.
- [x] Add focused tests for ConfigMap key safety, runtime image/probes/readiness, metrics parsing, bounded samples/events, and concise redacted CLI output.
- [x] Implement typed evidence collection, optional memory sampling, source ConfigMap key resolution, and failure output.
- [x] Run focused and full Go tests; fresh whole-branch review remains before push.
- Ruling: memory metrics remain best-effort and do not gate verification — metrics.k8s.io may be absent or denied in real clusters, so treating it as required would turn telemetry coverage into a false repair verdict — cost if wrong: memory curves may be missing when an operator expects them.
- Ruling: source ConfigMap values are never emitted or retained; only object/key presence is stored — values can contain credentials despite ConfigMap semantics — cost if wrong: evidence may miss a value-level misconfiguration.
