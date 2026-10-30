---
layout: default
title: Verification and delivery
description: Understand shadow resources, local patches, cluster apply, and GitOps pull request delivery.
permalink: /guides/delivery/
section: guides
---

# Verification and delivery

The delivery mode decides where a reviewed change goes. It does not replace patch review, Kubernetes authorization, or rollout observation.

## Shadow verification

**Effect:** Reads cluster evidence and bounded logs by default, then creates a per-run namespace. Fixora proves bidirectional NetworkPolicy enforcement before it creates the candidate Pod, applies the candidate patch there, and removes the namespace afterward unless `--keep-shadow` is set. No production workload is changed by this option alone. Use `--include-logs=false` to skip log reads.

```bash
kubectl fixora fix deployment/api -n prod --container api --image ghcr.io/example/api:v1.2.3 --shadow --delivery patch
```

The clone omits workload identity and production dependencies. Both ingress and egress are denied. Fixora first runs positive and negative TCP controls; if the CNI does not enforce standard NetworkPolicy, API/object inventory is incomplete, or an active cluster-wide/CNI-specific policy may affect the run, no candidate Pod is created. An installed extension API with no active policy objects does not block by itself. The probe image must be built from `build/service-probe.Dockerfile`, published through your approved registry process, and configured as a digest-pinned reference in `FIXORA_SHADOW_PROBE_IMAGE`. Required ConfigMaps and image-pull secrets are not copied, so affected candidates report **inconclusive**. Secret and persistent storage references remain blocked. `--shadow-egress allow` is rejected. Inspect the reported status; `--quick` skips default shadow verification.

Shadow results include bounded failure evidence: requested and runtime image identity, termination and memory request/limit, configured probe endpoint (without its host), source ConfigMap/key presence (never ConfigMap values), and observed Ready timing. During verification Fixora also samples candidate container memory from `metrics.k8s.io/v1beta1` every 15 seconds, up to 32 samples. Metrics are optional diagnostics and never change the verification verdict; if the API or permission is unavailable, the output says so. To enable the curve, grant `get` on the `pods` resource in API group `metrics.k8s.io` for the shadow namespace(s). Probe events are redacted, truncated, and capped before they are included.

Long-running candidates must become Ready within 60 seconds of Pod creation, then remain Running and Ready for two continuous minutes with no container restarts. A missed startup allowance or any observed failure rejects the candidate. If `--shadow-timeout` or an interruption ends observation before the checks finish, the result is **inconclusive** and Fixora does not revise or retry the patch automatically. The default `--shadow-timeout` is ten minutes; increase it for slow API operations, while the Ready startup allowance remains 60 seconds. Job/CronJob clones skip the stability window but must reach `Succeeded`; a still-running Pod is not a pass.

Shadow verification requires cluster-scope permission to create, read, list, and delete uniquely labeled namespaces and list nonstandard cluster-wide policy APIs, plus namespaced ServiceAccount read and Pod/NetworkPolicy read, list, create, and delete permissions in the source and run namespaces. Namespace deletion is UID-guarded and awaited. `--keep-shadow` retains the isolated namespace for inspection; the namespace name and deny policy are reported.

Build and publish the fixed probe image with your approved registry workflow, then configure its immutable digest:

```bash
docker buildx build --platform linux/amd64 \
  -f build/service-probe.Dockerfile \
  -t registry.example/fixora/service-probe:reviewed --push .
docker buildx imagetools inspect registry.example/fixora/service-probe:reviewed
export FIXORA_SHADOW_PROBE_IMAGE='registry.example/fixora/service-probe@sha256:<64-character-manifest-digest>'
```

Choose the platform matching the Kubernetes nodes and use the published manifest digest. Do not use a mutable tag in `FIXORA_SHADOW_PROBE_IMAGE`. The probe image has only a fixed TCP listener mode and the IP-only service probe; it has no shell, credentials, API client, or DNS lookup. `hack/e2e-shadow-isolation.sh` creates a separate kind cluster with Calico, builds and digest-pins this image, runs the E2E suite, then removes only the cluster and registry it created.

## Local patch

`--delivery patch` writes a patch for inspection. It does not update the production object. Keep the output with your incident record and review it before using another tool to apply it.

## Cluster apply

**Effect:** Reads cluster evidence and bounded logs by default. After eligibility checks, confirmation, and a server-side dry-run when enabled (the default), writes to the target production workload. It can trigger a rollout. Use `--include-logs=false` to skip log reads.

```bash
kubectl fixora fix deployment/api -n prod --container api --image ghcr.io/example/api:v1.2.3 --shadow --delivery cluster
```

In **v0.8.0**, a successful apply is the end of this command: Fixora does not monitor the live rollout or offer a rollback. Watch the rollout with your normal Kubernetes, Helm, or GitOps tooling and follow your team's rollback runbook if health degrades. Automatic post-apply health checks and a reviewed rollback path are **Upcoming** on the development branch. Ensure your RBAC permits the exact target write and shadow resources.

## Source or pull request

**Effect:** Reads cluster evidence and bounded logs by default, creates and cleans up a temporary shadow Pod and NetworkPolicy, and reads source in `--repo`. A PR delivery writes the source, commits a branch, and may push/open a GitHub PR or GitLab MR. It does not directly apply the production workload. Use `--include-logs=false` to skip log reads.

```bash
kubectl fixora fix deployment/api -n prod --container api --image ghcr.io/example/api:v1.2.3 --repo ./platform --gitops --shadow --delivery pr --yes
```

In v0.8.0, PR delivery requires `--yes` to acknowledge the remote Git operation; other review prompts may still appear. Managed workloads can reject direct cluster apply and route you to source review. Source mapping must be unambiguous; validation failures should stop delivery. Review the rendered result, diff, base branch, repository permissions, and remote before approving a PR. See [source validation]({{ '/guides/source-and-coordinate/' | relative_url }}).

## Approved Service routing repairs

`fix Service/<name> --service-policy <local-file>` uses a dedicated routing verifier rather than a workload clone. See [Service repairs](service-repairs.md) for local intent and the fixed probe image. Preview and JSON/YAML output create no verifier or production resources.

`fix pvc/<name> --pvc-policy <local-file>` is a separate exact-intent flow for unbound class assignment or bounded volume expansion. It uses a PVC server-side dry-run and live binding/capacity observation; it never mounts production storage in a shadow. See [PVC repairs](pvc-repairs.md) for policy examples and the non-rollbackable expansion limits.

A successful temporary-Service TCP/HTTP contract permits a guarded selector/targetPort patch for an unmanaged ClusterIP Service. The production gate repeats the contract against the real Service; it does not establish dependent application recovery. Failed gates offer an explicit field-only inverse. `--yes` never silently authorizes rollback. Helm Service values translation remains manual review. Raw/Kustomize source delivery produces a reviewed patch and retains the existing source/Git validation flow; a temporary routing contract does not certify a rendered source change or reconciliation.
