---
layout: default
title: Source validation and coordination
description: Inspect raw manifests, Helm charts, and Kustomize overlays; learn about upcoming coordinated fixes.
permalink: /guides/source-and-coordinate/
section: guides
---

# Source validation and coordination

Fixora can inspect local source before a delivery. Use the same repository revision and kube context that your deployment process will use.

## Inspect and validate source

**Effect:** Reads local files and may perform a server-side dry-run when requested; it does not apply a production workload.

```bash
kubectl fixora repo ./platform
kubectl fixora validate ./platform
kubectl fixora lint -f ./platform
kubectl fixora preflight -f ./platform
```

`repo` identifies raw, Helm, or Kustomize source. `validate` renders and validates; `lint` checks the manifests or chart; `preflight` includes policy and Kubernetes dry-run checks. Rendered validity does not guarantee rollout health or parity with another environment. Use `policy-check -f` for the production policy checks separately.

## Coordinated fixes — Upcoming

`coordinate` and its alias `fix-set` exist on [codex/production-hardening](https://github.com/baka126/fixora-cli/tree/codex/production-hardening), not in v0.8.0. They plan ordered fixes across related resources, fail closed on preflight, and require consent for a partial reverse rollback. `--from` can derive a related set from one resource. Review each step and its write scope before applying. Do not use this section with a v0.8.0 binary.

**Effect:** On the development branch, reads related resources; an approved coordinated apply can write multiple cluster objects.

```bash
kubectl fixora coordinate --from deployment/api -n prod --preview
```
