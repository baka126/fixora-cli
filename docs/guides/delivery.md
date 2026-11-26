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

The clone omits workload identity and production dependencies. Both ingress and egress are denied. Fixora first runs positive and negative TCP controls; if the CNI does not enforce standard NetworkPolicy, API/object inventory is incomplete, or an active cluster-wide/CNI-specific policy may affect the run, no candidate Pod is created. An installed extension API with no active policy objects does not block by itself. The probe image must be built from `build/service-probe.Dockerfile`, published through your approved registry process, and configured as a digest-pinned reference in `FIXORA_SHADOW_PROBE_IMAGE`. For a required `env[].valueFrom.configMapKeyRef`, Fixora copies only the exact referenced `data` keys into minimal same-name ConfigMaps in the shadow namespace, up to 32 keys and 64 KiB total. Copied values are never included in Fixora evidence or retry context, and candidate logs, event messages, and probe-failure event evidence are withheld because an application or probe could print those values. Fixora verifies the created ConfigMap response has the exact requested key/value map; admission changes make verification inconclusive. Missing maps/keys and optional refs are inconclusive; `envFrom`, ConfigMap volumes, projected ConfigMap volumes, Secret refs, and image-pull secrets are not copied. ConfigMaps can contain credentials; `--keep-shadow` retains copied key contents with the sandbox, so delete the namespace after inspection if they should not remain. Otherwise namespace cleanup removes them automatically. Persistent storage references remain blocked. `--shadow-egress allow` is rejected. Inspect the reported status; `--quick` skips default shadow verification.

Shadow results include bounded failure evidence: requested and runtime image identity, termination and memory request/limit, configured probe endpoint (without its host or query), source ConfigMap/key presence (never ConfigMap values), Secret reference metadata only (never Secret data), and observed Ready timing. The failure-specific MVP modules cover OOM memory curves, ConfigMap configuration references, probe stability, and image runtime identity. Only a passing module can preserve a verified verdict for its matching failure; unavailable or insufficient evidence is inconclusive. Results preserve the original structural Pod-spec parity score and add a separate operational parity score: each distinct unavailable required ConfigMap or isolated Secret reference costs 15 points, an identified but untested service/database dependency costs 15, missing failure-specific module coverage costs 10, and untested production traffic costs 10. Unknown dependency or reference state is listed as a limitation instead of receiving an invented deduction. During verification Fixora also samples candidate container memory from `metrics.k8s.io/v1beta1` every 15 seconds, up to 32 samples. For an OOMKilled finding only, the candidate must show at least 20% memory-limit headroom and a flat observed trend across three or more distinct samples spanning at least 30 seconds; a candidate OOM or measured unsafe curve fails, while missing or incomplete samples make verification inconclusive. Metrics remain diagnostics for other finding types. If the API or permission is unavailable, the output says so. To enable the curve, grant `get` on `pods.metrics.k8s.io` through an appropriately scoped ClusterRole/ClusterRoleBinding, since per-run shadow namespaces are created dynamically; this permission can read Pod metrics across the namespaces covered by that binding. Probe events are redacted, truncated, and capped before they are included.

Long-running candidates must become Ready within 60 seconds of Pod creation, then remain Running and Ready for two continuous minutes with no container restarts. Together, the startup allowance and stable soak make a three-minute verification horizon. A missed startup allowance or any observed failure rejects the candidate. If `--shadow-timeout` or an interruption ends observation before the checks finish, the result is **inconclusive** and Fixora does not revise or retry the patch automatically. The default `--shadow-timeout` is ten minutes; increase it for slow API operations, while the Ready startup allowance remains 60 seconds. AI-assisted fixes allow at most two redacted revisions (three total candidates); explicit operator commands, policy-bound repairs, and inconclusive attempts are never revised. Job/CronJob clones skip the stability window but must reach `Succeeded`; a still-running Pod is not a pass.

During text-mode verification, Fixora updates a compact progress line in an interactive terminal. Use `--plain` to print line-oriented progress for CI logs and redirected output. Structured JSON/YAML output does not include progress lines.

## Combined dependency shadow — Upcoming

On the development branch, `coordinate --from <root>` can verify a connected multi-workload graph in one per-run namespace before any coordinated production write. The graph is bounded to same-namespace Deployments, StatefulSets, DaemonSets, exact ConfigMap/Secret references, and selector-based ClusterIP Services with TCP ports. Workload candidates are all created before readiness observation so connected candidates can depend on one another during startup. Services use run-specific selectors, and EndpointSlice target references and resolved target ports must match the exact ready shadow Pod UIDs and container ports.

The graph namespace has default-deny ingress and egress. A verified graph policy permits only same-run graph traffic and DNS to the explicitly discovered `kube-system/kube-dns` Service Pods. Runtime probes must establish same-run connectivity and deny external ingress and egress. Missing DNS, unsupported policy APIs, an unenforced NetworkPolicy CNI, incomplete discovery, a failed candidate, endpoint mismatch, or unexpected shadow objects prevents a graph certificate and production dry-run.

Fixora copies only referenced ConfigMaps and Secrets. Secret values stay out of Fixora output and certificates, and any Secret-bearing graph namespace is deleted even when `--keep-shadow` is set. Without Secrets, `--keep-shadow` may retain a successful graph namespace. The identity needs read access to exact source dependencies and permission to create/delete namespaced ConfigMaps and Secrets in the temporary namespace, in addition to the namespace, Pod, Service, NetworkPolicy, EndpointSlice, and policy-inventory permissions used by the graph verifier.

`--preview` creates no shadow resources and reports shadow/live verification as false. A completed shadow verifies each candidate Pod's readiness/stability and each included Service's exact shadow endpoints. External systems, production traffic, persistent storage/data, production rollout health, and dependency authentication are not certified. `--from` graph delivery remains Upcoming pending live acceptance on a disposable cluster with an enforcing CNI.

PVC-backed graph shadows are an explicit opt-in through `--storage-shadow-policy <local-file>`. Fixora performs read-only preflight, then requires the operator to type `SNAPSHOT` after reviewing the exact source PVC UIDs, requested and maximum logical capacity, source/snapshot/restore classes, crash-consistency behavior, temporary ReferenceGrant scope, and possible provider charges. `--yes` does not bypass this second consent. After confirmation, Fixora creates a source snapshot and exact temporary ReferenceGrant, restores separate claims in the run namespace, admits the candidate Pods before waiting for `WaitForFirstConsumer` binding, and verifies CSI driver, PV identity, capacity, and node topology. Runtime restore proves provisioner support; an API dry-run alone is insufficient. Persistent-data runs are always deleted and cannot be retained. These CSI snapshot capabilities remain Upcoming until the tagged disposable-cluster acceptance passes. See [PVC snapshot acceptance](../testing/graph-pvc-snapshot-acceptance.md).

Use `kubectl fixora cleanup` to preview isolated shadow run namespaces retained with `--keep-shadow` or left behind after an interrupted run. The command only selects namespaces with Fixora's sandbox label, session label, source-namespace annotation, and generated `fixora-shadow-` name; temporary NetworkPolicy witness namespaces are excluded. The preview is read-only. Add `--yes` to request deletion; Fixora re-reads each namespace, uses UID and resource-version preconditions, and waits for deletion to finish. The command applies to the current Kubernetes context, so pass `--context` when needed. It never scans or deletes resources in workload namespaces.

Shadow verification requires cluster-scope permission to create, read, list, and delete uniquely labeled namespaces and list nonstandard cluster-wide policy APIs, plus namespaced ServiceAccount read and Pod/NetworkPolicy read, list, create, and delete permissions in the source and run namespaces. ConfigMap key verification additionally requires `get` on source ConfigMaps and `create` on ConfigMaps in the run namespace. Namespace deletion is UID-guarded and awaited. `--keep-shadow` retains the isolated namespace, deny policy, and copied ConfigMap data for inspection; the namespace name and deny policy are reported.

## Open a shell in a verified shadow Pod

`--shadow-shell` opts into a shell offer after Fixora reports a successful shadow verdict. Fixora requires interactive text output on a TTY, rechecks the generated shadow namespace and Pod UIDs plus their run labels, and checks `kubectl auth can-i create pods/exec` before opening `/bin/sh`. It prompts before starting the shell. The shell runs only in the isolated candidate Pod; ingress and egress remain denied, and commands are never run automatically. Shell edits do not change the saved patch or production resource.

After the shell exits, Fixora deletes the namespace by default and waits for Kubernetes deletion to complete. Add `--keep-shadow` to retain the namespace after exit; you then own cleanup and can use the existing `kubectl fixora cleanup` workflow. If you decline the shell, the namespace follows the same retention rule. `--shadow-shell` cannot be used with `--preview`, `--quick`, JSON output, or a non-interactive terminal.

**Additional permission:** the current identity needs `create` on `pods/exec` in the per-run namespace. This is separate from the read/create/delete permissions needed for shadow verification and grants interactive command execution in that namespace. Avoid granting it broadly if operators should not open shells in candidate workloads.

**Effect:** Runs the normal guarded shadow verification, then may open an interactive shell only after confirmation and identity checks. Namespace cleanup depends on `--keep-shadow`; no production object is modified by the shell.

```bash
kubectl fixora fix deployment/api -n prod --shadow-shell
kubectl fixora fix deployment/api -n prod --shadow-shell --keep-shadow
```

Build and publish the fixed probe image with your approved registry workflow, then configure its immutable digest:

**Effect:** Builds a local image, publishes it to the configured registry, inspects the published manifest, and writes an environment variable in the current shell. It does not modify Kubernetes resources.

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

In **v0.8.0**, a successful apply is the end of this command: Fixora does not monitor the live rollout or offer a rollback. The current development branch observes readiness or completion for supported Pods, Deployments, StatefulSets, DaemonSets, Jobs, and CronJobs after cluster apply. If verification fails, Fixora reports bounded events and cause hints, then offers an exact resource-aware rollback when one can be derived. It never rolls back automatically. The rollback needs a separate interactive confirmation; with `--yes`, Fixora leaves it unapplied and prints the command. Unsupported resource kinds, inconclusive reads, and timeouts remain unverified and require normal operator observation. These checks do not establish application SLOs, dependency health, or production traffic. Ensure your RBAC permits the exact target write and verification reads.

## Source or pull request

**Effect:** Reads cluster evidence and bounded logs by default, creates and cleans up a temporary shadow Pod and NetworkPolicy, and reads source in `--repo`. A PR delivery writes the source, commits a branch, and may push/open a GitHub PR or GitLab MR. It does not directly apply the production workload. Use `--include-logs=false` to skip log reads.

```bash
kubectl fixora fix deployment/api -n prod --container api --image ghcr.io/example/api:v1.2.3 --repo ./platform --gitops --shadow --delivery pr --yes
```

In v0.8.0, PR delivery requires `--yes` to acknowledge the remote Git operation; other review prompts may still appear. Managed workloads can reject direct cluster apply and route you to source review. Source mapping must be unambiguous; validation failures should stop delivery. Review the rendered result, diff, base branch, repository permissions, and remote before approving a PR. See [source validation]({{ '/guides/source-and-coordinate/' | relative_url }}).

## Approved Service routing repairs

`fix Service/<name> --service-policy <local-file>` uses a dedicated routing verifier rather than a workload clone. See [Service repairs](service-repairs.md) for local intent and the fixed probe image. The non-Helm preview and JSON/YAML paths create no verifier or production resources; Helm preview with `--yes` can run and clean up a temporary route verifier.

`fix pvc/<name> --pvc-policy <local-file>` is a separate exact-intent flow for unbound class assignment or bounded volume expansion. It uses a PVC server-side dry-run and live binding/capacity observation; it never mounts production storage in a shadow. See [PVC repairs](pvc-repairs.md) for policy examples and the non-rollbackable expansion limits.

A successful temporary-Service TCP/HTTP contract permits a guarded selector/targetPort patch for an unmanaged ClusterIP Service. The production gate repeats the contract against the real Service; it does not establish dependent application recovery. Failed gates offer an explicit field-only inverse. `--yes` never silently authorizes rollback. Raw/Kustomize source delivery produces a reviewed patch and retains the existing source/Git validation flow.

Helm-managed Service routing can use the source-only values workflow below. It supports selector-managed ClusterIP Services with TCP ports, requires the local `--service-policy`, an explicit chart/release/render context, and a `--helm-map` for each changed routing field. Fixora renders the chart twice before editing and requires identical output, then accepts only the exact authorized selector and selected port `targetPort` changes. Ambiguous port identity, chart render instability, unsupported Service variants, or collateral rendered changes require manual review.

**Effect:** Reads the live Service and chart, performs two unchanged renders and one proposed render plus admission dry-run. `--preview` never writes values; without `--yes` it creates no temporary verifier resources, and with `--yes` it runs and cleans up the temporary route verifier.

```bash
kubectl fixora fix Service/api -n team \
  --service-policy ./policies/api-service.yaml \
  --repo ./platform \
  --helm-chart charts/api --helm-release api \
  --helm-values charts/api/values-prod.yaml \
  --helm-edit-values charts/api/values-prod.yaml \
  --helm-render-mode upgrade \
  --helm-map /spec/selector=/serviceSelector \
  --helm-map /spec/ports/0/targetPort=/serviceTargetPort \
  --preview
```

Review the listed values keys, then replace `--preview` with `--delivery=pr --yes` to run the temporary routing verifier and write the certified values change for review. Fixora performs admission validation and the verifier before writing; it does not run `helm upgrade`, apply the Service, or trigger GitOps reconciliation. Output reports reconciliation as pending. The temporary verifier proves only the policy-declared route from its probe to captured Ready backends, not production consumer recovery.
