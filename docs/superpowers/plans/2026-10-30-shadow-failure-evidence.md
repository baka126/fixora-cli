# Shadow Failure-Specific Evidence Plan

1. Add failing tests for ConfigMap key resolution without value disclosure, image/runtime identity, probe endpoint and readiness timing, and best-effort metrics parsing.
2. Implement typed evidence records and source ConfigMap resolution for dependencies omitted by the sandbox.
3. Sample Pod memory metrics during shadow verification, correlate candidate container status/events with images, probes, and Ready timestamps, and preserve evidence on inconclusive outcomes.
4. Add concise text evidence while retaining structured JSON/YAML data; document metrics API behavior and RBAC limitations.
5. Run focused and full Go tests, inspect diff, and record branch progress.
