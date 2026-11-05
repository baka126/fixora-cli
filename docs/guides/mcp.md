---
layout: default
title: MCP server
description: Connect an AI assistant to Fixora's local MCP tools with an explicit permission boundary.
permalink: /guides/mcp/
section: guides
---

# MCP server

Fixora can run a local MCP stdio server for assistants. The assistant inherits the Kubernetes access of the local process and the chosen kubeconfig; use a dedicated read-only service account or context for routine investigations.

**Effect:** Starts a local stdio process. Tool calls can read cluster resources, Events, logs, and local non-secret configuration. The v0.8.0 MCP tool set does not apply a production patch.

```bash
kubectl-fixora serve --mcp -n prod --context my-cluster
```

Configure your MCP client to launch `kubectl-fixora` with `serve`, `--mcp`, namespace, and context arguments. Do not put a shell pipeline between the client and stdio server. The process runs until the client closes it.

## Tools in v0.8.0

| Purpose | Tools |
| --- | --- |
| Investigation | `analyze`, `incidents`, `health`, `runbook` |
| Plan review | `plan-fix`, `preview-fix`, `validate-fix` |
| Cluster reads | `list-resources`, `get-resource`, `get-logs`, `list-events` |
| Discovery | `list-filters`, `config` |

The release implementation passes resource and log reads through the caller's Kubernetes permissions. It does **not** enforce the later branch's typed allowlist and redaction on every MCP read. A client with broad RBAC can therefore receive sensitive resource bodies or logs. Restrict RBAC and use a trusted client; do not expose the stdio process as a network service.

## Upcoming hardening

The [development branch](https://github.com/baka126/fixora-cli/tree/codex/production-hardening) narrows resource reads, bounds and redacts logs/Events, and adds explicit namespace checks. Its `--mcp-shadow` switch exposes `shadow-verify` only at server startup; each call also requires `confirm=true` after operator approval. That tool creates temporary shadow resources and uses deny-egress, patch-only delivery. It is not in v0.8.0 and does not apply to production.

For production use, inspect the exact release binary and tool list your MCP client sees. Review the [safety model]({{ '/safety/' | relative_url }}) before granting a client cluster access.
