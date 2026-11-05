# Step 3: proof-bound verification policies

Status: draft for user review; product implementation has not started.
Date: 2026-10-05.
Baseline: controller-parity checkpoint `a9bc256` on `codex/controller-repair-parity`.

## Purpose and constraints

Extend Fixora's ability to repair real workload failures dynamically through dedicated startup, placement, security and coordinated verification policies. Preserve the working Deployment demos, multi-provider AI contract, existing common repair families, and the controller delivery gates from step 2.

Without AI, explain the observed cause and uncertainty and infer only changes established by concrete evidence. With AI, offer a workload-specific proposed patch and validate its actual fields; the model's confidence and narrative do not establish operator intent. No scenario names, images, commands, UID values, ports or node labels are built into repair decisions.

A repair is supported only when it has positive tests, ambiguous/unsafe refusal tests, a dedicated verification result, and delivery/rollback evidence. This work extends specific proven repairs, not every crash, Pending Pod, or security finding.

## Selected approach

Implement four independently testable policy slices on the existing planning/shadow/delivery paths. A shared proof envelope binds the proposal to current Kubernetes identities and exact changed fields. Each family supplies its own authority checks, validators and observations. Existing image/resources/env/probe/filesystem policies retain their behavior.

Alternative: expanding the generic allowlist alone is smaller, but Pod readiness cannot establish placement intent, startup provenance or a safe coordinated rollback. Alternative: keeping all new families source-review-only preserves existing safety but does not extend verified repair coverage. The selected approach uses source review as the fallback when a family's proof cannot be established.

Implementation order: startup, placement, security, coordinated execution. Finish each slice's regression and refusal coverage before enabling its delivery path. Inline execution remains the user's previously selected execution method.

## Shared proof contract

A policy proof records:

- canonical target kind/name/namespace/UID and the failing Pod UID when present;
- the current template fingerprint and a normalized, exact candidate patch digest;
- the changed field family and selected container identities;
- authority provenance, such as an explicit baseline, existing effective runtime UID, or enforced namespace policy;
- causal observations linked to the target/Pod UID and relevant current controller revision;
- family-specific observations and caveats from shadow verification;
- the live preflight snapshot used for guarded server dry-run and apply.

Proofs are created by Fixora, never accepted as authoritative from an AI response or an unchecked serialized CLI input. A saved plan/result requires revalidation before execution. Changing a candidate invalidates its previous proof. Execution rechecks relevant live configuration; UID, template or policy changes require fresh analysis. Node/policy evidence is checked for current applicability, not accepted merely because it appears in an old event.

The step 2 UID/resourceVersion guarded-apply path remains mandatory. Required verification cannot be bypassed with quick, yes or force flags. Source-managed resources retain source-level delivery. Existing Job/ReplicaSet mutation and Pod immutability restrictions remain in effect.

## 3a. Trusted startup restoration

### Behavior

Extend the existing `--startup-baseline` flow to supported workload envelopes and a uniquely identified failing regular container in a multi-container workload. Use the shared kind-aware template reader/wrapper. Preserve current Deployment command-only behavior and the exact command/args pin through verification.

An operator-supplied matching manifest remains the startup authority. The baseline must identify the current kind/name/namespace, selected container and exact current image reference. An unambiguous current crash and matching live workload/Pod configuration are required. A mutable image reference is not historical digest proof; the supplied manifest is an operator assertion of intended startup, and shadow observes that startup with the current image.

Only the selected container's command/args change. Other containers, init containers, resources, env, probes, volumes and security controls remain unchanged. Multi-container selection uses the failing-container evidence; multiple failing/ambiguous candidates require review. The existing explicit-baseline flag remains compatible.

AI may explain why restoration is appropriate and suggest evidence to locate an intended command. It cannot invent a server/sleep command, turn `exit 1` into a keepalive, or revise the operator's approved startup patch. Automatic selection of a historical ReplicaSet/release without demonstrated startup provenance is outside this slice.

### Verification and delivery

Validate the exact baseline-derived patch, launch the existing safe isolated shadow, require readiness/soak or batch completion as appropriate, and retain startup-specific identity/provenance in the result. Do not weaken Secret/PVC/service-account/host-access source restrictions. StatefulSet/DaemonSet rolling-update gates apply; Job/ReplicaSet/Pod proposals stay source/recreation review. CronJob templates affect future Jobs and remain pending future-run verification.

### Acceptance examples

Positive: different applications with distinct baseline commands, multiple containers with one proven failure, Deployment/StatefulSet/DaemonSet/CronJob envelopes, and concrete Job/ReplicaSet/Pod review proposals.
Refusal: wrong image/identity, stale Pod, ambiguous failing container, malformed/multiple-document baseline, secret references, recovered workload, arbitrary AI command, or revised baseline fields.

## 3b. Placement and toleration repairs

### Authority and candidate scope

Introduce a dedicated placement policy rather than enabling scheduling fields for existing strategies. Reuse resource-request repairs for proved capacity issues; this new family covers proved selector/required-node-affinity mismatches and exact missing `NoSchedule` tolerations.

Add one shared optional `--repair-baseline <manifest>` authority input for placement/security. It supplies intended values from an operator-approved workload manifest. A baseline must match target and selected container/image identities. An explicitly present field authorizes its exact intended value; an absent field grants no permission to remove a constraint. If a baseline has changes in multiple repair families, require `--repair-policy placement|security` to select one family, or offer a source-review proposal rather than silently applying a combined change. For an unambiguous single-family baseline this flag is optional. It selects a policy; it does not override that policy's proof or delivery gates. The existing startup flag remains compatible and command restoration stays separately pinned.

The model may produce a placement patch dynamically. Direct eligibility requires the candidate's changed placement fields to agree with the approved baseline. Without an intent baseline, provide the candidate and the specific missing proof for source review.

Initial automatic fields: a nonempty exact nodeSelector map, bounded required nodeAffinity using explicit label-key/value predicates, and exact `Equal`/key/value/`NoSchedule` toleration additions. Preserve existing tolerations, especially eviction-related entries, and all unrelated scheduling fields. Required affinity restoration uses baseline equality rather than AI inference about a preferred pool. No automatic wildcard/`Exists` tolerations, NoExecute changes, nodeName/schedulerName/priority/preemption changes, pod affinity/anti-affinity, topology spread or direct node taint/label mutations. Control-plane and node-health/pressure taint bypasses remain review-only even if a broad candidate is proposed.

### Causal and verification evidence

Require current unschedulable-Pod evidence linked to the target UID and the candidate's changed predicate. List live nodes; demonstrate how current placement excludes them and how the approved change permits compatible, Ready, schedulable nodes. Check image OS/architecture and all remaining applicable placement predicates. Unreadable/unsupported constraints or another unresolved blocker do not count as a proven repair.

Create the patched shadow with the approved placement constraints preserved. Require a scheduler-assigned eligible node, current matching labels/taints, application readiness and soak. Do not force nodeName to make verification pass. Recheck placement proof at delivery, then verify live updated replicas and their actual placement.

Direct placement delivery initially covers eligible Deployment and StatefulSet rollouts. DaemonSet placement changes produce source-review proposals until fleet-wide placement verification exists; one scheduled shadow cannot establish every selected node's safety. CronJob changes are future-template-only, with future scheduling verification pending. Job/ReplicaSet/Pod mutations remain review-only.

### Acceptance examples

Positive: approved selector correction, approved required-affinity correction, exact approved missing NoSchedule toleration, dynamic label/taint names and values.
Refusal: unrelated scheduler cause, unreadable node/policy data, conflicting affinity, incompatible image architecture, pressure/control-plane taints, wildcard tolerations, unsupported topology constraints, recovered Pod, stale policy or missing operator placement intent.

## 3c. Security policy repairs

### Separate operational failures from hygiene

Keep the current ephemeral writable-cache policy intact. A PermissionDenied message does not authorize removing readOnlyRootFilesystem, using root/privileged execution, adding capabilities or changing persistent-volume ownership.

Add a dedicated security-strengthening policy for current admission/runtime failures with a specific field-level cause. Namespace Pod Security labels and current target-owned FailedCreate/admission evidence must be readable and agree with the diagnosis. Link failures to the current revision: historical events and an unrelated ReplicaSet's failure cannot override a healthy workload. Explicit hygiene recommendations remain review-oriented rather than being presented as repaired incidents.

Initial automatic candidates:

- require `runAsNonRoot: true` when an existing explicit effective positive runAsUser already establishes the same non-root execution identity;
- `allowPrivilegeEscalation: false`, `seccompProfile.type: RuntimeDefault`, and `capabilities.drop: [ALL]` only when the exact change is supported by enforced Pod Security Admission and an operator-approved matching baseline;
- preserve existing effective UID/GID, command, env, image, service account, volume identity and unrelated security fields.

Root-to-nonroot UID/GID selection, new capabilities, Unconfined/Localhost seccomp, privileged/host-process mode, readOnlyRootFilesystem changes (which require a separate custom-policy/operational proof), SELinux/AppArmor/sysctl changes, fsGroup ownership changes to persistent data, RBAC/service-account changes and Windows security semantics remain source review. Do not bypass the existing privileged/elevated-capability shadow-source restrictions; those original sources require review even when the proposed patch removes their privileges.

AI chooses workload-specific values/fields only within the allowed policy and provided evidence. No UID, group, cache path or application-name shortcuts are embedded in code. If the baseline or causal proof is missing, retain a reviewable proposal with one concise explanation of what is missing.

### Verification and delivery

Validate the field-level strengthening and authority, server dry-run the proposed template, and create the patched shadow in the same namespace/admission environment. Check admitted effective security settings, unchanged application identity, readiness/soak, and logs/restarts for the original failure signal. Admission acceptance alone does not prove application recovery. A shadow pass covers observed behavior, not every later application operation.

The existing controller mutation, fleet caveat and batch/future-run gates apply. Stateful storage/security ownership changes remain outside automatic verification. Verify live admitted Pods and the expected settings after an eligible rollout.

### Acceptance examples

Positive: a declared non-root UID with a missing runAsNonRoot requirement; approved restriction fixes with target-linked admission failures; multi-container selection and parent-controller binding.
Refusal: root UID, guessed identity, missing enforced-policy proof, stale admission event, persistent data ownership change, security relaxation, original privileged source, unrelated healthy-workload hygiene or source-managed direct delivery.

## 3d. Verified coordinated repairs

### Scope

Make the existing coordinated command use the same analysis, evidence enrichment, dynamic AI candidate selection, deterministic inference and policy checks as single-resource fix. Do not add a second weaker interpretation of AI plans. Every selected step must have its own bound policy proof and exact verified patch.

Initially enable finite sets of independently shadow-verifiable Deployment/StatefulSet/DaemonSet repairs. Preserve explicit user-provided ordering. Derived graph ordering requires actual Kubernetes resource references rather than names/text similarity. Cycles, ambiguous relationships and repairs whose verification requires another un-applied candidate use source review; combined isolated dependency environments belong to later cross-resource work.

CronJobs, existing Jobs, ReplicaSets and standalone Pod image changes stay outside automatic coordinated rollback initially. Their future-run, immutability, recreation or restart semantics do not support the same verified rollback contract.

### Execution and outcomes

Before any production mutation: collect all selected candidates, verify every shadow, validate dependencies/order and patch identity, check source ownership, capture the exact changed fields, perform all required live preflights and guarded server dry-runs, and present the concrete set for the existing explicit apply confirmation. Missing/failed verification aborts the set with zero production mutations; clean up all shadows/artifacts.

At each apply, recheck identities and relevant configuration using a current validated snapshot, apply the exact verified patch, and require an explicitly successful live verification. Unknown/skipped/pending is unverified, not healthy. Stop on failure; report which steps actually mutated.

Rollback remains explicitly consent-gated. Replace whole-manifest restore in this verified path with a field-scoped inverse of the exact applied changes. Verify the same target UID and that the fields still match Fixora's applied values; use the current resourceVersion mutation precondition. Restore only fields changed by that step, in reverse application order, preserving unrelated edits and deletions. If another actor changed those fields or the target was replaced, refuse automatic restore and report the concrete conflict. Never claim a Kubernetes transaction or atomic rollback.

Reports distinguish candidate verification, applied, live verified, unverified, rollback available, rollback refused/conflicted and rollback verified/failed. Text is concise; proof details remain behind --proof/structured output. Noninteractive --yes authorizes apply according to existing semantics, not implicit rollback or unsafe-policy overrides.

### Acceptance examples

Positive: two dynamic independent controller repairs, exact verified patch reuse, explicit and proven derived order, field-scoped rollback preserving unrelated edits.
Refusal/failure: one failed shadow before mutation, missing proof, forged/replayed result, changed candidate/UID/template, circular references, dependent shadow requiring a future mutation, second-step failure, unavailable live verification, rollback refusal and concurrent rollback conflicts.

## Validation and completion

For every slice, write positive and unsafe/ambiguous refusal tests before implementation. Exercise proof creation, candidate validation, shadow observations, live preflight, guarded delivery and rollback/reporting through real planning/CLI seams. Reuse provider-independent AI replay tests rather than spending provider credits for each field variant.

Retain all existing demo/controller/provider regressions. Run full Go unit tests, affected/full race tests as justified, vet, CLI build and E2E compilation. Test immutable/future-only outcomes as intentionally supported review/pending states, not false successful recovery.

Use fake clients and deterministic event/node/image fixtures for the full matrix. Optional live EKS validation uses a disposable namespace and small synthetic fixtures, never resets fixora-demo or modifies node taints/labels/policy. Clean up test resources and report live coverage separately. No Terraform/state/private demo files are committed. Update remediation inventory/reference with actual supported outcomes and limits.

Each implementation slice gets a reviewed checkpoint. Step 3 is complete only when all four slices and their reporting/rollback gates pass the acceptance matrix. A design/spec alone is not completion of the feature.

## References and boundaries

Kubernetes references checked 2026-10-05:

- [Assigning Pods to Nodes](https://kubernetes.io/docs/concepts/scheduling-eviction/assign-pod-node/): selectors and required affinity determine eligible placement.
- [Taints and Tolerations](https://kubernetes.io/docs/concepts/scheduling-eviction/taint-and-toleration/): toleration permits a tainted node but does not guarantee scheduling; eviction effects require separate treatment.
- [Pod and Container Security Context](https://kubernetes.io/docs/tasks/configure-pod-container/security-context/): Pod/container settings and effective identity/permissions must be evaluated at their proper scope.

Helm values/rendered source mapping remains step 4. Storage clones, dependency-service shadow environments, scaling/disruption/network/node/operator mutation, automatic batch replay, universal fleet-placement simulation and image-history-based command inference remain later work. No provider transport/model/output-contract changes are required by this design.
