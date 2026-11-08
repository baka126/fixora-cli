---
layout: default
title: Dashboards and integrations
description: Use terminal views, optional ecosystem detection, and explicit local analyzer extensions.
permalink: /guides/dashboards-and-integrations/
section: guides
---

# Dashboards and integrations

The terminal views use the same local kubeconfig as other Fixora commands. Use a narrow namespace where possible and leave a watch view with Ctrl-C.

**Effect:** Reads incident and cluster state; does not write Kubernetes objects.

```bash
kubectl fixora ui -n prod
kubectl fixora ui --tui -n prod
kubectl fixora cluster
```

In v0.8.0, running `kubectl fixora` with no command opens the older dashboard. A command chooser is **Upcoming** on [the development branch](https://github.com/baka126/fixora-cli/tree/codex/production-hardening).

## Optional integrations

**Effect:** Detects local tools and relevant CRDs; it does not install integrations or change workloads.

```bash
kubectl fixora integrations
kubectl fixora filters -o json
```

Detection is not a guarantee that a particular analyzer has enough RBAC or data to diagnose an incident. Gateway API, KEDA, Kyverno, Trivy Operator, OLM, and other optional APIs are useful only where installed. The [analyzer catalog]({{ '/reference/analyzers/' | relative_url }}) separates enabled defaults from opt-in checks.

## Custom analyzers

`custom-analyzers list|add|run` registers and executes explicitly chosen local executables. Treat an extension as code running with your local user and kubeconfig permissions. Review its source, arguments, and data flow before adding it.

**Effect:** Reads and updates local analyzer registration; `run` executes a local program. The extension's own behavior can have additional effects.

```bash
kubectl fixora custom-analyzers list
```
