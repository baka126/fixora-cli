# Policy-Bound HPA Repairs Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Diagnose HPA target and metric failures and apply only exact operator-approved missing resource requests to supported workload targets with guarded verification and rollback.

**Architecture:** Keep HPA diagnosis in `internal/analyzer`; add a dedicated `internal/hpafix` package for strict policy loading, candidate preparation, a narrow typed runtime, preflight receipts, apply, observation, and inverse. Route exact HPA fixes through dedicated CLI handling so general workload shadow or source writers cannot authorize HPA changes.

**Tech Stack:** Go 1.26.6, Kubernetes client-go 0.31.4 typed autoscaling/apps/core APIs plus the existing dynamic client for `metrics.k8s.io/v1beta1`, `yaml.v3` strict policy decoding, existing CLI and Fixora AI response contracts, fake client-go clients, disposable kind acceptance where available.

**Spec:** `docs/superpowers/specs/2026-10-18-hpa-repairs-design.md`

## Global Constraints

- No demo names, labels, images, commands, or resource values are hardcoded.
- The only cluster candidate adds missing `resources.requests.cpu` and/or `resources.requests.memory` fields to the named containers in the approved target's Pod template.
- Policy values are exact operator intent; AI cannot modify or approve them.
- Never automatically create/change `scaleTargetRef`, `minReplicas`, `maxReplicas`, metrics, scaling behavior, replica count, or a workload's image, command, probes, security, placement, storage, or labels.
- Default output is review-only. Direct delivery requires the approved HPA policy, an explicit cluster delivery selection, an unmanaged target, and a fresh private receipt.
- Do not automatically undo a confirmed request change because a later rollout or metrics check fails.
- Preserve local Terraform/state/demo files and commit only Fixora product code, docs, tests and harnesses.

## Review Focus

- An HPA whose `status.observedGeneration` lags its metadata generation must remain stale/unverified even when conditions appear healthy; pin this in Task 1 diagnosis tests.
- A valid policy with one correct container plus one unknown or duplicate container must not partially authorize a request patch; pin this in Task 2 policy tests.
- A StatefulSet or Deployment replaced under the same name between preflight and write must fail UID checks; pin this in Task 3 runtime tests.
- A rollout that succeeds while HPA metrics remain unavailable must report partial completion, not repair success; pin this in Task 4 observation tests.
- A structured preview, AI-returned request, or source-managed workload must not write or serialize authority; pin these in Task 5 CLI tests.

---

### Task 1: Precise HPA diagnosis

**Files:**
- Modify: `internal/analyzer/hpa.go`
- Modify: `internal/analyzer/hpa_readerror_test.go`
- Create: `internal/analyzer/hpa_diagnosis_test.go`
- Modify: `test/coverage/remediation.json`
- Modify: `docs/reference/remediation-coverage.md`

**Interfaces:**
- Consumes: `Analyzer.analyzeHPATargets(*ScanContext) ([]Finding, error)` and generic resource maps returned by `kube.Reader`.
- Produces: stable HPA status names for missing/incomplete targets, unsupported target kinds, invalid specs, stale status, unavailable metrics, invalid targets and missing resource requests. Existing `Finding` JSON fields stay compatible.

- [ ] **Step 1: Write failing diagnosis tests** named `TestHPAObservedGenerationStale`, `TestHPAMetricsConditionsRemainDistinct`, `TestHPAMissingRequestsOnlyForUtilization`, and `TestHPAReadFailuresRemainUnavailable`. Assert exact status, target identity, metric evidence, and no conversion of API errors into absence.
- [ ] **Step 2: Run the focused tests and confirm they fail** with the current analyzer collapsing condition/metric states or omitting the new evidence.
- [ ] **Step 3: Implement precise HPA condition and metric classification** in `internal/analyzer/hpa.go`. Read `status.observedGeneration`; inspect HPA conditions and current metric entries; emit missing-request findings only for `Resource` metrics with `target.type=Utilization`; retain target read errors separately from NotFound. Do not add request values or a repair candidate here.
- [ ] **Step 4: Run `go test ./internal/analyzer ./test/coverage`** and confirm all focused diagnosis and inventory tests pass.
- [ ] **Step 5: Commit** as `feat(analyzer): diagnose HPA target and metric states`.

### Task 2: Strict HPA policy and exact request candidate

**Files:**
- Create: `internal/hpafix/types.go`
- Create: `internal/hpafix/policy.go`
- Create: `internal/hpafix/policy_open_unix.go`
- Create: `internal/hpafix/policy_open_windows.go`
- Create: `internal/hpafix/plan.go`
- Create: `internal/hpafix/policy_test.go`
- Create: `internal/hpafix/plan_test.go`

**Interfaces:**
- Produces: `LoadPolicy(path string) (*Authority, error)`; `Prepare(ctx context.Context, r Reader, policy *Authority, f analyzer.Finding) (Plan, error)`; `Plan.PatchYAML() (string, error)`; `Plan.check() error`.
- `HPARepairPolicy` YAML uses `apiVersion: fixora.io/v1alpha1`, `kind: HPARepairPolicy`, `metadata.name`, `metadata.namespace`, and `spec.hpa.name`, `spec.target.kind`, `spec.target.name`, `spec.requests[]` entries `{container, cpu?, memory?}`. Each request entry contains at least one exact Kubernetes quantity.
- `Reader` has `GetHPA(context.Context, string, string) (*autoscalingv2.HorizontalPodAutoscaler, error)`, `GetWorkload(context.Context, string, string, string) (map[string]any, error)`, `ListPods(context.Context, string) ([]corev1.Pod, error)`, `ListHPAs(context.Context, string) ([]autoscalingv2.HorizontalPodAutoscaler, error)`, `ListPodMetrics(context.Context, string) ([]map[string]any, error)`, and `ResolveOwner(context.Context, corev1.Pod) (kube.WorkloadOwner, error)`.
- `Plan` privately binds HPA/target identity, original and candidate workload templates, policy digest, changed request paths, evidence snapshot and a plan digest. Public plan fields contain display data only.

- [ ] **Step 1: Write failing policy tests** named `TestLoadPolicyStrictSchema`, `TestLoadPolicyRejectsDuplicateKeysAndSpecialFiles`, `TestLoadPolicyRejectsInvalidQuantitiesAndDuplicateContainers`, and `TestPolicyRecheckRejectsReplacement`. Assert unknown/multiple YAML documents, duplicate keys, oversized/nonregular inputs, path replacement and invalid Kubernetes quantities fail closed.
- [ ] **Step 2: Run the focused policy tests and confirm the missing loader rejects none of the new contract cases.**
- [ ] **Step 3: Implement `LoadPolicy` and private `Authority`** in `policy.go` with strict one-document decoding, the schema in the Interfaces block, a 1 MiB limit, no-follow platform opens, exact-byte digest and explicit revalidation.
- [ ] **Step 4: Run `go test ./internal/hpafix -run 'TestLoadPolicy|TestPolicyRecheck'`** and confirm all policy tests pass.
- [ ] **Step 5: Write failing candidate tests** named `TestPrepareAddsOnlyPolicyApprovedMissingRequests`, `TestPrepareRejectsUnknownOrDuplicateContainer`, `TestPrepareRejectsNonUtilizationAndUnsupportedTarget`, and `TestPrepareRequiresExactHPAAndTargetIdentity`. Include generated workload/container names and mixed missing/existing requests.
- [ ] **Step 6: Run candidate tests and confirm they fail** before candidate preparation exists.
- [ ] **Step 7: Implement `Prepare` and immutable `Plan`** in `plan.go`. Support only same-namespace Deployment/StatefulSet targets and CPU/memory `averageUtilization`; add exact approved values only to missing regular-container request paths; reject no-op, unknown container, mismatched identity, and any other changed field.
- [ ] **Step 8: Run `go test ./internal/hpafix`** and confirm all policy and candidate tests pass.
- [ ] **Step 9: Commit** as `feat(hpafix): validate approved HPA request candidates`.

### Task 3: Typed evidence snapshot and non-mutating preflight

**Files:**
- Create: `internal/hpafix/client.go`
- Create: `internal/hpafix/snapshot.go`
- Create: `internal/hpafix/verify.go`
- Create: `internal/hpafix/verify_test.go`

**Interfaces:**
- Consumes: `Reader`, `Authority`, and immutable `Plan` from Task 2.
- Produces: `NewClient(k *kube.TypedClient) (Runtime, error)`; `Verify(ctx context.Context, r Runtime, p Plan) (*Receipt, error)`; opaque single-use `Receipt`.
- `Runtime` embeds `Reader` and adds only `PatchWorkload(context.Context, string, string, string, []byte, bool) (map[string]any, error)`.

- [ ] **Step 1: Write failing tests** named `TestSnapshotRequiresStableOwnedReadyPods`, `TestSnapshotPreservesPodMetricsReadFailure`, `TestOperationContextPreservesParentDeadline`, `TestVerifyDryRunsExactTemplatePatch`, `TestVerifyRejectsAdmissionRewrite`, `TestVerifyRejectsReplacedTargetUID`, `TestVerifyRejectsHPAOrMetricsDrift`, and `TestReceiptCannotBeCopiedOrReused`. Assert no non-dry-run mutation is called.
- [ ] **Step 2: Run the focused verifier tests and confirm they fail** before the runtime and preflight exist.
- [ ] **Step 3: Implement `typedRuntime`** in `client.go` using typed AutoscalingV2, AppsV1 and CoreV1 clients plus `TypedClient.Dynamic.Resource(schema.GroupVersionResource{Group:"metrics.k8s.io", Version:"v1beta1", Resource:"pods"})` for PodMetrics. Resolve owner chains by UID; preserve API errors; implement target-template JSON Patch dry-run with UID/resourceVersion tests and strict returned-object decoding.
- [ ] **Step 4: Implement `collectSnapshot`** in `snapshot.go` to read exact HPA, target, all namespace Pods and HPAs, PodMetrics, owner chain, readiness, rollout state and HPA status. Refuse unreadable, incomplete, stale, scaling, multiple-target and unsupported evidence. Add `OperationContext(parent context.Context) (context.Context, context.CancelFunc)` with one three-minute default deadline from the start of the exact-target operation, preserving any earlier parent deadline; the CLI creates it once and passes that context through preparation, preflight, apply, observation and rollback.
- [ ] **Step 5: Implement `Verify` and receipt consumption** in `verify.go`. Compare the server-admitted target against the candidate, recollect all bound evidence and policy bytes, then issue a private shared-state single-use receipt. No create/delete/scale/eviction methods are added.
- [ ] **Step 6: Run `go test ./internal/hpafix`** and confirm all preflight tests pass.
- [ ] **Step 7: Commit** as `feat(hpafix): verify HPA repairs before delivery`.

### Task 4: Guarded apply, HPA observation, and explicit inverse

**Files:**
- Create: `internal/hpafix/delivery.go`
- Create: `internal/hpafix/delivery_test.go`
- Modify: `internal/hpafix/client.go`

**Interfaces:**
- Consumes: `Plan`, `Receipt`, `Runtime` and bound evidence from Task 3.
- Produces: `Apply(ctx context.Context, r Runtime, p Plan, receipt *Receipt) (*Applied, Outcome, error)`; `Observe(ctx context.Context, r Runtime, a *Applied) (Outcome, error)`; `Rollback(ctx context.Context, r Runtime, a *Applied) (Outcome, error)`.
- `Applied` privately owns deep copies of the original and admitted target, changed paths, HPA identity, and the resulting resourceVersion.

- [ ] **Step 1: Write failing delivery tests** named `TestApplyConsumesReceiptAndPatchesApprovedPaths`, `TestApplyLostResponseIsNotRetried`, `TestObserveSeparatesRolloutFromHPAHealth`, `TestRollbackRestoresOnlyAddedRequests`, and `TestRollbackRejectsChangedRequestOrUID`.
- [ ] **Step 2: Run delivery tests and confirm they fail** before guarded apply/observe/inverse exist.
- [ ] **Step 3: Implement `Apply`** with one atomic JSON Patch, receipt consumption, policy/evidence revalidation, strict response identity/field checks, and read-after-lost-response classification. Never retry or automatically roll back an uncertain write.
- [ ] **Step 4: Implement `Observe`** under the operation deadline. Require target current-generation rollout and all desired replicas Ready, HPA `status.observedGeneration` matching metadata generation, `ScalingActive=True`, and a current supported metric. Report workload convergence separately from autoscaler health and partial writes accurately.
- [ ] **Step 5: Implement explicit `Rollback`** guarded by the original target UID and current candidate values. Restore only request paths added by Fixora; preserve unrelated edits and reject conflicts.
- [ ] **Step 6: Run `go test ./internal/hpafix`** and confirm all delivery tests pass.
- [ ] **Step 7: Commit** as `feat(hpafix): apply and observe approved request repairs`.

### Task 5: Dedicated CLI, AI context, concise output, and source refusal

**Files:**
- Create: `internal/cli/hpa_fix.go`
- Create: `internal/cli/hpa_fix_test.go`
- Modify: `internal/cli/root.go` (options, flag registration, dispatch, and advanced help)
- Modify: `internal/cli/help.go`
- Modify: `internal/cli/ai_repair.go`

**Interfaces:**
- Consumes: exact HPA `Finding`, optional AI assessment, `--hpa-policy`, existing delivery flags, and `hpafix` outcomes.
- Produces: `runHPAFix`, `runHPAFixRuntime`, option validation, concise text/proof rendering, and output behavior consistent across text/JSON/YAML.

- [ ] **Step 1: Write failing CLI tests** named `TestHPAFixPreviewNeverWrites`, `TestHPAFixRequiresPolicyAndExplicitClusterDelivery`, `TestHPAFixRejectsMixedRepairPolicies`, `TestHPAFixAIProposalCannotChangePolicyValues`, `TestHPAFixBlocksSourceManagedTargets`, and `TestHPAFixOutputIsConciseAndStructured`. Assert no file/cluster writes from preview or structured output and no serialized receipt.
- [ ] **Step 2: Run focused CLI tests and confirm they fail** before the dedicated route exists.
- [ ] **Step 3: Add `--hpa-policy` parsing and exact-HPA dispatch** in `root.go`; reject mixed HPA, PDB, Service and generic repair policy inputs; preserve all current workload routes.
- [ ] **Step 4: Implement `runHPAFixRuntime`** in `hpa_fix.go`: show concise diagnosis, policy-backed exact candidate, explicit verification/delivery state and one next step; route source-managed objects to manual source instructions with no source writes.
- [ ] **Step 5: Add sanitized workload/HPA context to AI preparation** and validate any returned patch against the exact policy and target-template allowlist. AI failure or malformed output must retain deterministic diagnosis and never grant authority.
- [ ] **Step 6: Update short and advanced help** with `--hpa-policy` and one example; preserve the concise help style.
- [ ] **Step 7: Run `go test ./internal/cli ./internal/hpafix ./internal/analyzer`** and confirm HPA-specific and existing dispatch tests pass.
- [ ] **Step 8: Commit** as `feat(cli): deliver policy-bound HPA request repairs`.

### Task 6: Acceptance harness, coverage inventory, and final regression

**Files:**
- Create: `hack/e2e-hpa.sh`
- Create: `test/e2e/fixtures/hpa-repair/`
- Create: `test/e2e/hpa_repair_test.go`
- Modify: `test/coverage/remediation.json`
- Modify: `docs/reference/remediation-coverage.md`
- Create: `docs/guides/hpa-repairs.md`
- Create: `docs/superpowers/reports/2026-10-18-hpa-repairs.md`

**Interfaces:**
- Consumes: analyzer statuses, policy, CLI and runtime behavior from Tasks 1–5.
- Produces: an isolated opt-in kind acceptance harness; updated named remediation profiles; user-facing policy/limits guide; durable report with executed and skipped checks.

- [ ] **Step 1: Write failing harness safety tests** named `TestHPAHarnessChecksPrerequisitesBeforeCreate` and `TestHPAHarnessRequiresDedicatedContext`. Assert it refuses an arbitrary current context before any mutation and only manages its uniquely named local kind cluster.
- [ ] **Step 2: Implement the harness and fixtures** using a generated Deployment, `autoscaling/v2` HPA, policy file, and metrics-server prerequisite. Keep cluster creation behind explicit opt-in and cleanup limited to harness-owned resources.
- [ ] **Step 3: Update the coverage inventory and guide** with exact diagnosis, candidate, policy and verification limits. Document that the policy fixes only approved missing requests and does not prove future scale-out capacity or application SLOs.
- [ ] **Step 4: Run targeted acceptance and project checks:** `go test ./test/coverage ./internal/hpafix ./internal/analyzer ./internal/cli`; `go test ./...`; `go test -race ./...`; `go vet ./...`; `make build`; `go test -c -tags='e2e,e2e_delivery' -o /tmp/fixora-hpa-e2e.test ./test/e2e`; `bash -n hack/e2e-hpa.sh`; and `git diff --check`. Run live kind only if Docker and kind are available; otherwise record it as skipped.
- [ ] **Step 5: Review repository status and commit only Fixora files** as `test(hpafix): cover policy-bound HPA repairs`.

## Spec coverage check

- Diagnosis statuses, exact target identity, HPA conditions, observed generation, request-utilization scope and unavailable evidence: Task 1.
- Strict independent intent, request-only candidate, quantity/schema validation, AI non-authority and private plan binding: Task 2.
- Typed runtime, complete owned Pod/HPA snapshots, dry-run admission comparison, fresh reads and one-use receipt: Task 3.
- Guarded write, uncertain response, rollout/HPA observation, deadline, explicit field inverse and partial completion: Task 4.
- Consent, structured-output side-effect protections, concise AI-aware UX, source ownership and help: Task 5.
- Coverage, local-cluster acceptance and durable limits: Task 6.

No feature or verification gate in the approved spec is intentionally deferred. The kind run is conditional on local prerequisites; when skipped, fake-client tests and E2E compile do not count as live controller validation.
