---
layout: default
title: Fixora CLI User Guide
description: The ultimate interactive K8s forensic and auto-remediation tool.
---

# 🚀 Fixora CLI User Guide

Welcome to the **Fixora CLI**! This tool empowers DevOps and SRE teams to instantly diagnose complex Kubernetes failures using AI, correlate issues across the cluster graph, and safely generate self-healing infrastructure patches.

---

## 📦 Installation

### Option 1: Krew (Recommended)
You can install the plugin via [krew](https://krew.sigs.k8s.io/):
```bash
kubectl krew install fixora
```

### Option 2: Binary
Download the latest binary from the GitHub Releases page and move it to your `$PATH`:
```bash
curl -sLO https://github.com/baka126/fixora-cli/releases/latest/download/kubectl-fixora
chmod +x kubectl-fixora
sudo mv kubectl-fixora /usr/local/bin/
```

---

## ⚡ Quick Start

To run a quick, read-only AI scan on your current namespace, simply run:
```bash
kubectl fixora analyze
```

**Targeting a Specific Failing Resource:**
If you know a specific pod or deployment is failing, point Fixora directly at it for a much deeper, localized scan:
```bash
kubectl fixora analyze pod/failing-pod-123 -n production
```

---

## 🖥️ The Interactive TUI Dashboard

Fixora ships with an incredible, `k9s`-style Terminal User Interface (TUI). This is the best way to triage active incidents.

To launch the dashboard, run:
```bash
kubectl fixora ui
```

### ⌨️ TUI Power-User Hotkeys

Mastering these hotkeys will turn you into an incident response wizard:

| Hotkey | Action | Description |
|--------|--------|-------------|
| `n` | **Switch Namespace** | Opens a fuzzy-finder overlay to quickly switch cluster namespaces without leaving the app. |
| `1`-`9` | **Switch Tabs** | Jump between Workloads, Security, Storage, Logs, and Fix Plans. |
| `/` | **Filter** | Instantly fuzzy-filter the incident table to find specific deployments. |
| `i` | **AI Root Cause** | Runs the configured AI provider against the selected incident and attaches a structured root cause and recommended fix. |
| `z` | **Zoom Modal** | Expands the right-hand detail pane to fill your entire terminal window. Great for reading massive AI summaries. |
| `l` | **Live Logs** | Suspends the UI and drops you into a live `kubectl logs -f` tail stream of the failing container. |
| `e` | **Interactive Editor** | Suspends the UI and opens the AI's generated patch in your `$EDITOR` (e.g. `vim`). Customize the patch by hand and apply upon saving! |
| `s` | **Shadow Verify** | From the **Fix Plan** tab, shows the patch diff, asks permission, deploys an isolated shadow clone, waits for readiness, reports parity, and cleans up. |
| `a` | **Apply Fix** | When viewing the **Fix Plan** tab, pressing `a` applies an eligible patch through Fixora's confirmation and server dry-run gates. |
| `p` | **Push Review** | After a successful shadow verification, writes the source patch from `--repo`, commits a branch, pushes it, and opens a GitHub PR or GitLab MR when `gh` or `glab` is installed. |
| `g` | **Graph Pivot** | On the Graph tab, select an upstream/downstream component and hit `Enter` to refocus the AI on that new component. |

---

## ⚙️ Advanced Scanning Flags

### Include Bounded Logs
Sometimes the AI needs to see standard output to know *why* an application crashed (e.g., a Null Pointer Exception or Python traceback).
```bash
kubectl fixora analyze --include-logs
```

### Redact Sensitive Data
If you are operating in a highly compliant environment, you can instruct Fixora to aggressively redact IP addresses, emails, API keys, and passwords before sending the payload to the LLM.
```bash
kubectl fixora analyze --redact
```

### Apply Fixes (Non-TUI)
If you prefer not to use the TUI, you can still apply AI-generated fixes directly from the command line:
```bash
kubectl fixora analyze pod/failing-pod --apply
```
This will present a secure, interactive prompt showing you the YAML diff before it modifies your cluster.

---

## 🔌 GitOps Compliance

Fixora respects GitOps! If you attempt to patch a workload that is actively managed by **Helm**, **ArgoCD**, or **Flux**, Fixora will automatically warn you that direct cluster patching will cause state drift. 

Instead, it will advise you on exactly which upstream repository or Helm `values.yaml` file needs to be updated.

---

## 🤖 AI Providers

Fixora supports multiple AI providers. You can configure this via the `FIXORA_AI_BACKEND` environment variable or the `--backend` flag.
- `local` (Default, uses the default cluster credentials)
- `openai` (Requires `OPENAI_API_KEY`)
- `gemini` (Requires `GEMINI_API_KEY`)
