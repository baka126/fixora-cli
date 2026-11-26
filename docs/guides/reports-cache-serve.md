---
layout: default
title: Reports, cache, and local API
description: Export incident evidence, manage local cache, and run Fixora's local HTTP endpoint.
permalink: /guides/reports-cache-serve/
section: guides
---

# Reports, cache, and local API

These features help integrate Fixora into an incident process. Check the contents of every exported artifact before sharing it; evidence can include workload names, configuration hints, and logs.

## Reports and bundles

**Effect:** Reads the target and prints a Markdown report to stdout. Adding `--out` writes a local file; `bundle` writes a local archive. No cluster mutation.

```bash
kubectl fixora report deployment/api -n prod
kubectl fixora bundle deployment/api -n prod --out incident.tgz
```

Use `-o json`, `yaml`, `markdown`, `sarif`, `junit`, or `prometheus` where the command supports that format. See [output reference]({{ '/reference/configuration-and-output/' | relative_url }}) for the distinction between diagnostic output and a delivery patch.

## Cache and memory

`cache path|stats|list|purge|clear` manages local cache metadata; `cache add|get|remove` manages K8sGPT-style remote-cache entries. `memory list|clear` manages local scenario memory. Removing cached items does not affect Kubernetes objects.

**Effect:** Reads local cache paths and metadata; no cluster writes.

```bash
kubectl fixora cache stats
kubectl fixora memory list
```

## Local HTTP serve

**Effect:** Starts a loopback HTTP API for local incidents/analyze requests. Requests can read the configured cluster scope; the server does not provide a production write endpoint.

```bash
kubectl fixora serve 127.0.0.1:8080 -n prod
```

Keep it on loopback. If another process can reach the port, set `FIXORA_SERVE_TOKEN` and restrict the kube context/RBAC. For assistant stdio integration, use the separate [MCP mode]({{ '/guides/mcp/' | relative_url }}).
