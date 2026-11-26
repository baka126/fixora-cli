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

For Claude Code or Gemini CLI, use `kubectl fixora mcp setup claude-code` or `kubectl fixora mcp setup gemini-cli` to print the host-native registration command. Add `--apply` to run the installed host CLI and update its configuration. The default scope is `user`; use `--scope project` for a project entry. Add `--context NAME` or `-n NAMESPACE` to pass cluster scope to the server. Without `--apply`, no host configuration is changed. Applying can replace an existing entry named `fixora`. Fixora does not handle the host's subscription sign-in or credentials.

After registration, check status with `claude mcp list` or `gemini mcp list`. To remove Fixora, use the host's `mcp remove` command for server `fixora` in the same scope. These checks verify the host-to-server connection; `kubectl fixora ai doctor --live` instead checks the AI provider configured in Fixora itself.

## Tools in v0.8.0

| Purpose | Tools |
| --- | --- |
| Investigation | `analyze`, `incidents`, `health`, `runbook` |
| Plan review | `plan-fix`, `preview-fix`, `validate-fix` |
| Cluster reads | `list-resources`, `get-resource`, `get-logs`, `list-events` |
| Discovery | `list-filters`, `config` |

The release implementation passes resource and log reads through the caller's Kubernetes permissions. It does **not** enforce the later branch's typed allowlist and redaction on every MCP read. A client with broad RBAC can therefore receive sensitive resource bodies or logs. Restrict RBAC and use a trusted client; do not expose the stdio process as a network service.

## Upcoming hardening

The current development branch narrows resource reads, bounds and redacts logs/Events, and adds explicit namespace checks. `serve --mcp --mcp-shadow` exposes `shadow-verify` only at server startup; each call also requires `confirm=true` after operator approval. It creates temporary shadow resources, uses default-deny ingress and egress, and never delivers a patch to production.

### Verified sandbox inspection (development branch)

Add `--mcp-open-sandbox` alongside `--mcp-shadow` to expose `open-sandbox` and `close-sandbox`. Opening requires `confirm=true`; Fixora retains the sandbox only after its normal verification and evidence gates pass. It rechecks the namespace and Pod UIDs, readiness, restart counts, and the default-deny NetworkPolicy before returning an opaque `sandboxId` and `execArgs` for an operator to run locally. MCP does not execute a shell command.

At most three sandboxes can be open for one MCP server process. Each expires after 30 minutes; closing requires `sandboxId` and `confirm=true`. Fixora also deletes open sandboxes when the MCP server shuts down. If the server process crashes or cleanup is denied, use `kubectl fixora cleanup` to inspect and remove the remaining Fixora namespaces. Opening a sandbox does not change or deliver the production patch.

### Create a pull or merge request (development branch)

Start MCP with `kubectl-fixora serve --mcp --mcp-pr-repo /path/to/repository` to expose the remote-writing `create-pr` tool. The tool is absent when this flag is not set. It uses the repository's currently checked-out branch; the repository must have one recognized GitHub or GitLab `origin`, exactly one matching push destination, a clean worktree, and at least one commit ahead of the requested base branch. Fetch the base branch before previewing so Fixora can compare against a current local ref.

First call `create-pr` with `confirm=false` and a base branch to see the branch, base commit, recent commits, and remote repository identity. Review that preview and provide its `previewId`, `branch`, `headCommit`, and `baseCommit` as `previewId`, `expectedBranch`, `expectedHeadCommit`, and `expectedBaseCommit` in the confirmed call. The handle expires after 10 minutes and can be used once. Fixora rechecks the clean worktree, branch, remote identities, and base commit; it rejects a changed preview and pushes the exact reviewed commit ID. A confirmed call then opens a GitHub pull request or GitLab merge request through the explicit repository API. Fixora does not edit, stage, or commit files, and does not change the cluster. The local repository path is not returned to the MCP client.

The PR description labels verification as an operator attestation. Fixora cannot prove that an arbitrary existing branch matches a prior shadow-verified patch. Review the branch diff and the supplied verification summary before confirming. If the push succeeds but opening the review request fails, the result reports the branch as pushed so it can be retried or opened manually.

For production use, inspect the exact release binary and tool list your MCP client sees. Review the [safety model]({{ '/safety/' | relative_url }}) before granting a client cluster access.
