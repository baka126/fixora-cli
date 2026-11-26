---
layout: default
title: Get started
description: Install v0.8.0, select a Kubernetes context, and complete a safe first investigation.
permalink: /get-started/
section: get-started
---

# Get started

Fixora is a local `kubectl` plugin. It uses your kubeconfig and your existing Kubernetes permissions; there is no Fixora account or hosted control plane.

## 1. Install a released binary

The installer downloads the release archive, verifies its checksum, and installs `kubectl-fixora` on your `PATH`. Review [the installer](https://github.com/baka126/fixora-cli/blob/main/scripts/install.sh) before running a remote script.

**Effect:** Downloads a v0.8.0 archive and writes a local executable. It does not access a cluster.

```bash
curl -fsSL https://raw.githubusercontent.com/baka126/fixora-cli/main/scripts/install.sh | VERSION=v0.8.0 sh
kubectl fixora version
```

You need `kubectl` and a working kubeconfig. Building from source requires the Go version in [`go.mod`](https://github.com/baka126/fixora-cli/blob/v0.8.0/go.mod). Helm or Kustomize are needed only for the matching source workflows. AI credentials are optional.

## 2. Choose the target deliberately

Check your active context and available permissions before reading production resources. `-n` limits a namespaced investigation; `--context` selects another kubeconfig context without changing your default.

**Effect:** Reads local kubeconfig, then reads cluster status and capability information.

```bash
kubectl config current-context
kubectl fixora status --context my-cluster -n payments
```

The [RBAC example](https://github.com/baka126/fixora-cli/blob/v0.8.0/docs/rbac.yaml) includes a read-only ClusterRole and a separate optional shadow Role. Tailor its permissions to your workflow. Missing permission or optional CRDs can reduce coverage.

## 3. Scan and explain

**Effect:** Reads workload status, Events, related resources, and bounded container logs in `payments` by default. It does not change cluster objects. Add `--include-logs=false` to each incident command when logs must not be read; that also removes log evidence from the diagnosis.

```bash
kubectl fixora scan -n payments --context my-cluster
kubectl fixora why deployment/payments-api -n payments --context my-cluster --proof
```

Read the proof and confidence alongside the proposed cause. The explanation can be incomplete when RBAC, Events, or workload history are unavailable. To request AI analysis, [configure a provider]({{ '/guides/ai/' | relative_url }}) and pass `--ai`; verify its answer against the cluster evidence.

## 4. Preview a concrete fix

Start with a resource and explicit patch inputs. A plan with unresolved placeholders may remain review-only.

**Effect:** Reads cluster evidence, including bounded logs by default, and prepares a local preview. Use `--include-logs=false` to skip logs. `--preview` does not apply a production patch or complete shadow verification.

```bash
kubectl fixora fix deployment/payments-api -n payments --container api --image ghcr.io/example/payments:v1.2.3 --preview
```

Continue with the [incident workflow]({{ '/guides/incident-workflow/' | relative_url }}) and [safety model]({{ '/safety/' | relative_url }}) before selecting a delivery mode.
