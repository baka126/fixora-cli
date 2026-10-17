# Step 5b: evidence-based PodDisruptionBudget repairs

Status: conversational scope approved; written spec awaiting review. No implementation is authorized by this checkpoint yet.
Starting checkpoint: `f7523a7`, branch `codex/service-dependency-repairs` (completed Step 5a).

## Intent and success criteria

Extend Fixora's dynamic Kubernetes diagnosis and remediation without regressing existing workload, provider, Service or source-delivery behavior. Deterministic mode must explain the observed cause and uncertainty. AI mode may add application-specific reasoning and propose concrete changes, but model output cannot establish availability intent or permission to change protection.

This slice covers PDB diagnosis and guarded correction of an existing PDB. It succeeds when Fixora can distinguish a broken selection or availability-policy mismatch from healthy, intentional disruption protection; produce an exact candidate when independently approved intent exists; validate it without disrupting workloads; and apply, observe or explicitly undo only its own changed fields. No application names, demo labels, commands or replica counts are hardcoded.

HPA repairs remain a separate next slice: scale-target identity, resource requests and metrics need their own verification contract. Storage, nodes, admission and operators remain later roadmap items.

## Current behavior and approach

`internal/analyzer/pdb.go` reports `DisruptionsBlocked` for zero expected Pods, insufficient healthy Pods, or zero disruptions allowed with a positive desired healthy count. Its four status counts cannot distinguish selection errors, stale status, controller failures, overlapping PDBs and intentional policy. `internal/fix/planner.go` has no dedicated repair path for this finding. The existing `pdb` strategy is review-only and must remain so unless the dedicated validation path grants authority.

Choose PDB-specific collection, policy validation and delivery alongside existing repair domains. Reuse cluster access, redaction, source ownership checks, provider contracts, concise output and guarded mutation patterns. Keep the PDB diagnosis usable without a repair-policy document.

Alternatives: start with HPA support, which improves autoscaling coverage but leaves disruption diagnosis unresolved; or combine both, which couples two different verification problems. Neither is included in this implementation. Inferring permission to relax a budget merely because a drain is blocked is explicitly rejected.

## Diagnosis and evidence

Read the target PDB, namespace Pods and PDBs, relevant controller chains, scale/replica information, conditions and events. Preserve forbidden, missing and failed reads as distinct unavailable evidence. Do not convert unreadable resources into empty lists or zero counts. Use bounded requests and cancellation throughout collection and verification.

Evaluate full label selectors using Kubernetes selector semantics, including match expressions. Distinguish an absent selector from an empty selector. Resolve actual Pod owner UIDs rather than name resemblance; follow Deployment ownership through ReplicaSets. Surface unrelated selected owners, terminating Pods, in-flight disruptions, rollout/scaling activity and overlapping PDB selection.

Give precise findings for: no selected Pods; selected unhealthy Pods; stale or failed PDB reconciliation; overlapping budgets; mixed or unsupported ownership; and mismatch with explicit approved intent. Multiple causes may coexist and must remain visible. Zero allowed disruptions by itself is an informational protection state, not proof that a policy needs relaxation. Never label an unmatched selector wrong without evidence of the intended workload. Existing consumers of `DisruptionsBlocked` receive a documented migration or compatible summary; do not silently break structured contracts.

Compare PDB status only when its observed generation matches the live generation and reconciliation is usable. Keep observed status, independently computed candidate effects and intended policy separate. Percentage and integer budgets must use Kubernetes-compatible rounding and validity rules, tested against the dependency version and a real API/controller where available. Invalid or unsupported forms produce diagnosis, not guessed arithmetic.

## Initial repair scope

- Existing `policy/v1` PDBs protecting one same-namespace Deployment or StatefulSet with a supported scale target. Diagnosis can describe broader cases; automatic repair is narrower.
- Correct a selector only when its current selection is empty and the approved replacement selects exactly the intended controller's complete stable Pod set. Refuse to remove protection from currently selected unrelated Pods. A selector that currently selects some intended Pods is diagnosis/review-only in this slice.
- Correct `minAvailable` or `maxUnavailable`, including a mutually exclusive switch, to an explicitly approved value. Either correction or both selector and budget correction may form one candidate.
- Preserve `unhealthyPodEvictionPolicy`, metadata, owner references and every unrelated field. A missing field retains its absence unless it is explicitly within the correction. Reject semantic no-ops.
- No PDB creation/deletion, overlap removal, label changes, controller edits, scale changes, node drains, Pod deletions/evictions, RBAC changes or unhealthy-Pod policy relaxation. No DaemonSet, standalone Pod, custom controller or multiple-owner automatic repair.
- No GitOps/Helm/operator-owned direct mutation. Source ownership ambiguity is a blocker.

## Independent availability intent

Introduce a strict local PDB repair policy, independently loaded from workload baselines and Service policy. The implementation plan must specify its concrete schema and CLI spelling without overloading existing loaders. Required content: exact PDB namespace/name; intended workload kind/name; exact approved selector; exactly one approved budget value; an absolute minimum healthy replica floor; and an absolute maximum number of concurrent voluntary disruptions. Require a positive minimum healthy floor and non-negative disruption ceiling. This intentionally excludes policies allowing every replica to be disrupted.

The document represents an operator's approved availability requirement, not an inferred application quorum. The same validators apply to deterministic and AI candidates. AI can propose a policy for review, but cannot silently write or load its own proposal as approval. Missing intent still permits root cause and reviewed suggestions, with one concrete prerequisite explaining why no apply is available.

Strict decoding rejects unknown fields, duplicate keys, invalid selector/budget forms and mismatched targets. Bind exact policy bytes, PDB/workload UIDs and relevant live configuration in private in-memory state. A filename, annotation, prior log, JSON report, AI confidence or bypass flag is not a verification receipt.

At the current replica count, compute the candidate's effective required healthy replicas and permitted disruptions. Require both approved limits, a complete healthy stable workload, and consistency with controller/PDB evidence. An HPA targeting this workload blocks direct repair in the initial slice; incomplete HPA listing also blocks it. Detect other scale/rollout changes through repeated snapshots. Do not edit or disable autoscaling. Future scaling can change the effective policy; verification is a point-in-time claim, not a guarantee of future availability or application quorum.

## Components and verification flow

Keep focused boundaries: analyzer evidence/classification; policy and candidate preparation; PDB verifier/runtime; CLI and source delivery. Add dedicated packages where the existing workload/Service types do not fit. Do not broaden their mutation allowlists or introduce a shared permissive receipt type.

1. Collect and bind PDB identity/spec/relevant metadata, intended controller identity/spec/generation, complete owned Pod membership/readiness, all namespace PDBs and autoscaler evidence. Candidate selection must cover exactly the intended stable Pod set and no other live Pods. A compatible selector for today's labels does not guarantee future labels.
2. Require observed controller convergence, desired replicas greater than zero, all desired replicas Ready, no terminating Pods, no in-flight disruptions and no overlapping PDB on any candidate Pod. Require an unpaused, completed Deployment rollout or completed StatefulSet revision/update. Initial implementation refuses active HPA targets and unsupported rollout modes. Existing unhealthy workload evidence directs users to repair the workload first, without recursively applying another repair.
3. Validate the exact candidate fields against approved intent and availability bounds. Perform server dry-run of the candidate update with identity/concurrency tests. Compare the admitted result with the intended spec and relevant metadata; unexpected admission changes refuse verification.
4. Re-read the relevant evidence after dry-run. Drift, unreadable evidence, new overlap, readiness loss or conflicting policy refuses authorization. Reuse one bounded operation deadline from the first read, respecting an earlier parent deadline.
5. Issue only a private in-memory preflight receipt binding candidate, policy and evidence. It authorizes the narrow delivery operation; it does not claim that Kubernetes evaluated a future eviction successfully. Recollect/revalidate before consumption. Preview and JSON/YAML diagnostic output never change cluster resources or source files.

Do not create a temporary PDB selecting production Pods: it could alter their protection. Do not send an eviction, including a dry-run eviction, as a substitute for validating the candidate. No synthetic traffic probe or workload shadow proves PDB enforcement. Server admission and independent arithmetic are preflight checks; live controller convergence is a separate post-apply check.

There is no atomic multi-resource transaction across PDB, controller, Pods and other budgets. Repeated reads narrow races but cannot eliminate concurrent changes. Any claimed result must disclose this limit; new health/overlap/scale drift during observation stops a verified-success claim.

## Apply, observation and rollback

Direct delivery requires an explicitly requested mutation of an unmanaged PDB and the private preflight receipt. Use a single atomic UID/resourceVersion-tested JSON patch for only selector and/or budget fields. Replace a selector exactly and remove the opposite budget field atomically when switching types. Preserve absent/null semantics and all unrelated fields. Check the returned resource against the admitted candidate.

If a write response is lost, read by exact identity and compare the edited fields and other bound intent. Report whether the candidate is now observed, unchanged, conflicting or unreadable; do not retry blindly or claim that observation proves who wrote it. Ambiguous outcomes require inspection and never trigger automatic rollback.

After a confirmed apply, wait within the remaining deadline for current-generation PDB status and recomputed selected membership/health to agree with the approved budget. Recheck overlap, controller scale/rollout and evidence availability. Report `policy converged` separately from `workload healthy` and from actual eviction/drain success, which is not tested. Timeout, admission failure, controller SyncFailed, concurrency or health loss reports partial completion and the observed remaining state. Never report no changes after a confirmed write.

Capture an inverse of only edited fields. Explicit rollback checks original PDB UID, current edited fields equal the applied candidate, and relevant intent before applying a resourceVersion-tested inverse. Refuse conflicting edits or ownership changes. Check the returned inverse fields and observe reconciliation; rollback can fail or conflict and must report that accurately. Never restore an old full object, scale workloads, evict Pods or automatically weaken another budget. Existing rollback consent behavior remains intact.

## AI, source delivery and user output

Providers receive relevant redacted PDB, workload and health evidence plus the approved policy's intended values when supplied. Reuse the existing provider response contract and separate explanation from patch eligibility. Validate model patches against the same exact fields and policy; confidence never bypasses checks. Do not change provider API/protocol behavior as part of this feature.

Use existing reviewed raw/Kustomize source workflows only where their file scope and validation can represent the PDB correction honestly. Keep them review-only, retain dirty-file/clean-commit checks, and distinguish source written from production reconciliation. Helm values suggestions remain manual: existing workload render certificates do not cover PDB effects. A runtime receipt cannot authorize a different source change.

Default output: resource/status, concise cause, proposed change or missing prerequisite, verification/delivery state and one next action. Full selectors, Pod ownership, arithmetic and raw controller evidence belong behind proof output. Intentional protection should explain why disruptions are unavailable without recommending relaxation. Avoid generic placeholder diffs or a long repeated incident/plan dump. Structured output exposes useful evidence and outcomes without serializing reusable authority.

## Acceptance and regression coverage

Diagnosis tests: nil/empty selectors, match expressions, no matching Pods, intentional zero disruption allowance, unhealthy/terminating Pods, stale observed generation, SyncFailed, unreadable resources, overlap and mixed owners. Do not test a read failure as an empty successful result.

Policy/arithmetic tests: strict schema, budget exclusivity, percentages and rounding, small replica counts, bounds, unsupported values, exact controller identity and no-op proposals. Use generated workload/label names to prove absence of demo coupling.

Positive delivery tests: healthy stable Deployment and StatefulSet, approved empty-selection correction, exact selector replacement, budget correction and type switch, preview no side effects, server admission, current-generation convergence, explicit field-only inverse. Verify every unrelated field survives.

Refusal/failure tests: absent intent, forged/replayed/stale receipt, replaced PDB/controller/Pod UID, changing replica count or readiness, active HPA, denied HPA/PDB reads, overlapping/extra selected Pods, active rollout/disruption, admission rewrite, deadline/cancellation from initial read, JSON patch conflict, lost response, post-write observation failure and rollback conflict. No failure path issues eviction or creates a temporary PDB.

Run full existing workload, controller, AI-provider, Service, source/Helm and CLI regressions. Add disposable local-cluster PDB/controller tests when Docker/kind are available; report skipped live validation honestly. Do not use the ONF/shared host or mutate EKS/demo workloads incidentally. Preserve untracked local infra/state/demo material; commit only product code/docs/tests.

## References and durable continuation

- Kubernetes PDB API semantics: https://kubernetes.io/docs/reference/kubernetes-api/policy/pod-disruption-budget-v1/
- Kubernetes PDB configuration and workload considerations: https://kubernetes.io/docs/tasks/run-application/configure-pdb/
- Roadmap: `docs/reference/remediation-coverage.md`
- Completed Step 5a: `docs/superpowers/reports/2026-10-14-service-dependency-repairs.md`

On written-spec approval, create a focused implementation plan with package/API/CLI choices, task dependencies and executable acceptance checks. Preserve the previously selected native inline execution preference when presenting that plan; do not assume approval of an unwritten plan. HPA design follows completion of this PDB slice.
