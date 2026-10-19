# Step 5c: policy-bound HPA workload repairs

Status: conversational scope approved; written spec awaiting review.
Starting checkpoint: `8aa73bd`, branch `codex/pdb-repairs` (completed Step 5b).

## Intent and success criteria

Extend Fixora's evidence-based Kubernetes repairs to HorizontalPodAutoscalers without regressing workload, Service or PDB behavior. Deterministic mode must explain what the API and workload evidence establish. AI may add a concise workload-specific assessment and propose alternatives, but model output cannot authorize resource values or autoscaler changes.

This slice covers precise HPA diagnosis and correction of missing resource requests on a supported scale target when an independent operator policy names the exact approved requests. It must identify HPA target and metrics failures, preserve uncertainty, and refuse to invent a target, replica bounds, utilization target, or resource request. A valid approved policy plus explicit delivery consent may authorize only the named missing request fields. The repair is successful only when the target workload rolls out and the HPA reports usable current metrics at its current generation. This is not a claim that the workload meets application SLOs or has capacity for every future scale-out.

No demo names, labels, images, commands, or resource values are hardcoded. Missing intent still yields useful diagnosis and an AI review proposal when evidence supports one.

## Current behavior and approach

`internal/analyzer/hpa.go` finds incomplete or absent scale targets and missing resource requests for resource utilization metrics. It treats target read errors separately from NotFound but does not explain HPA conditions or distinguish metrics API failures. The planner's literal `HPA` match misses `MissingResourceRequests`. Existing workload request inference is based on usage, OOM demand, or allocatable evidence; a single current metrics sample is not a durable capacity baseline.

Add a dedicated `internal/hpafix` policy and verification boundary, with HPA-specific evidence and CLI routing. Reuse readers, redaction, source ownership checks, concise output, target-template patch machinery, and guarded field update patterns where their contracts fit. Do not make the general workload strategy or shadow verifier authorize HPA changes.

Alternatives considered:

- AI-only advice avoids new mutation paths but leaves Fixora unable to carry an approved correction through verification.
- Automatically infer and apply resource requests from a current `kubectl top` sample or change HPA bounds from symptoms. Reject this: a point-in-time sample does not establish sustained demand, availability intent, or future cluster capacity.
- Apply exact request values from a separate operator policy, while keeping other HPA changes diagnostic or review-only. Choose this because it supports per-workload repair without embedding demo-specific values or trusting AI as approval.

## Diagnosis and evidence

For an exact HPA target, collect the complete `autoscaling/v2` object, its UID/generation/resourceVersion, scale target identity and live object, target controller/template identity, complete owned Pod set and readiness, HPA conditions/current metrics, and the relevant metrics API readings. Treat forbidden, unsupported API, NotFound, stale status, transient failure, and empty successful results distinctly. An unreadable list or metrics response is never an empty inventory or proof that metrics are absent.

Emit precise statuses for incomplete target references, missing target, unreadable target, unsupported target kind, invalid HPA spec, stale HPA status, metrics unavailable, target metric invalid, and resource requests missing for named containers/resources. Use HPA conditions such as `AbleToScale`, `ScalingActive`, and `ScalingLimited` as evidence, not as a replacement for reading the metric source. Preserve current and desired replica counts and metric target/current values. Explain when an average utilization metric cannot be computed because a selected container lacks a request.

Initial repair support is limited to `autoscaling/v2` HPAs targeting one same-namespace Deployment or StatefulSet, with a complete stable rollout and a resource `cpu` or `memory` metric using `averageUtilization`. Diagnosis may cover other target kinds and metric types, but they remain review-only. ContainerResource, Pods, Object, External, custom metrics, KEDA, and cross-namespace references do not enter automatic repair in this slice. Multiple HPAs targeting the same controller, active scaling, incomplete Pod ownership, unreadable metrics, a paused or incomplete rollout, or another concurrent controller change blocks delivery.

Metric observations are short-lived evidence. They may support an AI explanation or review-only suggestion; they do not create policy approval. Do not describe a few successful samples as a p95, workload SLO, or capacity forecast.

## Initial repair scope and independent intent

Introduce a strict local `HPARepairPolicy`, independently loaded from PDB, Service and workload baseline policies. The policy identifies the exact HPA namespace/name, target kind/name, and exact desired CPU/memory request values keyed by regular container name. The implementation plan will choose CLI spelling and final field representation without changing existing policy loaders. Require at least one desired request, reject entries for unknown containers or resources not needed by a supported utilization metric, and bind the policy bytes and live UIDs privately in memory.

The only cluster candidate adds missing `resources.requests.cpu` and/or `resources.requests.memory` fields to the named containers in the approved target's Pod template. Policy values are exact operator intent; AI cannot modify or approve them. Existing requests, limits, init containers, HPA spec, replicas, selectors, annotations, and unrelated fields must remain unchanged. Reject no-op candidates and any model patch with additional or differing fields. A policy mismatch, missing policy, or unsupported HPA condition still permits diagnosis and a review-only explanation.

Never automatically create/change `scaleTargetRef`, `minReplicas`, `maxReplicas`, metrics, scaling behavior, replica count, or a workload's image, command, probes, security, placement, storage, or labels. Do not infer any of these from names, workload size, current CPU/memory, or AI confidence.

Strict decoding rejects unknown fields, duplicate keys, multiple YAML documents, invalid Kubernetes quantities, empty or duplicate container names, invalid target kinds, and a policy target that differs from the live HPA. Resolve and read the file once; revalidate its exact bytes before delivery. Preview and structured output never serialize reusable authority.

## Verification boundary

Keep HPA verification in `internal/hpafix`; its runtime exposes only required reads, server-side dry-run, one guarded target-template update, and a field-only inverse. It must not expose scale, create/delete, eviction, or arbitrary patch operations.

1. Resolve the HPA target and exact owning controller/template by UID and owner chain. Require a complete live Pod inventory owned by the target, all desired replicas Ready, no terminating Pods, no active rollout, no current scaling transition, and no other HPA controlling the same target. Incomplete or conflicting evidence refuses delivery.
2. Validate the candidate against the policy and original target template. Preserve all non-approved fields and require at least one missing request to be corrected.
3. Server dry-run the exact target-template patch. Reject admission changes outside the approved request fields, identity changes, or any API error whose outcome cannot be established.
4. Re-read the HPA, target, Pods, policy bytes, metrics, and relevant controller state. Drift, stale generation, missing metrics, readiness loss, scale activity, or ownership change refuses authorization.
5. Issue an opaque, private, single-use, in-memory receipt bound to HPA and target UIDs/resourceVersions/generations, exact policy bytes, candidate, and evidence. Serialized reports, previews, AI output, copied receipts, or saved plans cannot authorize delivery.

Do not claim that a shadow Pod proves autoscaler behavior or replica capacity. This slice does not use a synthetic HPA or alter production scale settings to test a candidate.

## Delivery, observation, and rollback

Default output is review-only. Direct delivery requires the approved HPA policy, an explicit cluster delivery selection, an unmanaged target, and a fresh private receipt. Source-managed HPA or target workloads block direct cluster mutation. Reuse raw/Kustomize/Helm source workflows only if they can represent the exact target-template request effect; otherwise provide concrete manual source instructions and perform no source writes. Never label a source edit as a production repair before reconciliation.

Apply one atomic UID/resourceVersion-tested JSON patch to only the approved missing request paths in the target workload template. Validate the API response. A lost response is resolved by reading the exact target and comparing approved fields; do not retry blindly. If authorship or state is uncertain, report that and do not auto-rollback.

After a confirmed update, wait within one operation deadline for the target controller's current generation to roll out, all desired Pods to become Ready, the HPA to observe the current generation, `ScalingActive=True`, and its supported metric to be available. Recheck HPA target identity and replica state. If the HPA begins scaling or reaches a limit, report observed state and stop the success claim; do not alter HPA behavior or claim application recovery. A healthy target template and a functioning autoscaler are separate results.

Capture an inverse of only the newly added request fields. Explicit rollback checks the same target UID and current request values still equal Fixora's applied candidate; restore only those fields while preserving concurrent unrelated edits. Refuse changed ownership, policy, or edited fields. Report rollback observation and conflicts truthfully. Do not automatically undo a confirmed request change because a later rollout or metrics check fails.

There is no atomic transaction across the HPA, workload, Pods, metrics API, and admission controllers. Repeated evidence checks narrow races but cannot eliminate them. Report partial completion whenever a write occurred but convergence was not verified.

## AI and user output

Use existing provider-independent response contracts and redact relevant HPA, target, Pod, condition, and metric evidence. AI may explain why utilization is unavailable and propose a workload-specific value or source change for human review. It must distinguish observed usage from sustained demand and identify missing intent. Any AI-generated patch is checked against exact HPA policy and the narrow target-template allowlist; model confidence never grants delivery authority.

Default text stays concise: HPA identity/status, one evidence-based cause, approved proposed request change or missing prerequisite, verification result, and one next action. Detailed conditions, Pod membership, metric values and policy arithmetic belong behind proof. Do not emit generic placeholder diffs, a long incident narrative, or a healthy diagnosis when status is stale or unreadable.

## Acceptance and regression coverage

Diagnosis: missing/invalid target, target NotFound versus forbidden, unsupported target, stale generation, failed `AbleToScale`/`ScalingActive`, metric API failure, unknown metric, missing resource requests by container, utilization versus average-value semantics, multiple HPA targets, incomplete owner/readiness evidence, and healthy autoscaler.

Policy/candidate: strict schema, Kubernetes quantity validation, exact identity binding, missing/extra container rejection, exact approved values, only missing fields added, no-op refusal, generated names proving no demo coupling, AI patch constrained to policy, and absent policy retaining useful review output.

Delivery: preview/JSON/YAML no writes; source ownership refusal; stable Deployment and StatefulSet positive cases; server dry-run/admission comparison; stale/replayed/copied receipt rejection; HPA/target/Pod UID or generation change; partial metrics/read failures; scaling/rollout/readiness drift; guarded write conflict and lost response; current-generation rollout/HPA metric verification; explicit field-only inverse and rollback conflict. Assert no HPA spec or scale subresource is changed.

Run existing workload, controller, provider, Service, PDB, Helm-source and CLI regressions. Add disposable local-cluster acceptance for an HPA backed by metrics-server when Docker/kind and a compatible image are available. Report live-cluster validation as skipped when prerequisites are absent. Do not use ONF/shared hosts or mutate the user's EKS demo namespace. Preserve local Terraform/state/demo files and commit only Fixora product code, docs, tests and harnesses.

## References and continuation

- Kubernetes HPA behavior: https://kubernetes.io/docs/tasks/run-application/horizontal-pod-autoscale/
- HPA API reference: https://kubernetes.io/docs/reference/kubernetes-api/workload-resources/horizontal-pod-autoscaler-v2/
- Completed Service repairs: `docs/superpowers/reports/2026-10-14-service-dependency-repairs.md`
- Completed PDB repairs: `docs/superpowers/reports/2026-10-16-pdb-repairs.md`
- Roadmap: `docs/reference/remediation-coverage.md`

After this written spec is approved, write a focused implementation plan with exact schema/CLI details, package boundaries, receipt/runtime API, target patch mechanics, rollback, and executable acceptance checks. Preserve native inline execution for implementation. HPA support does not imply support for storage, networking, nodes, admission, or operators; those remain separate repair phases.
