# Evidence-based PDB Repairs Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Preserve the user's previously selected native execution method. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Diagnose disruption protection precisely and deliver approved PDB selector/budget corrections with bounded preflight, guarded apply, truthful observation and explicit field-only rollback.

**Architecture:** `internal/pdbfix` owns independent evidence, intent, candidates and mutation authority, without importing analyzer/CLI or widening existing repair gates. An analyzer adapter maps its evidence into findings; a dedicated CLI path supplies redacted intent to the existing AI contract and handles PDB-specific delivery. Typed Kubernetes access supplies fresh, complete repair inventories; scan filtering never substitutes for them.

**Tech Stack:** Existing Go 1.26.6, Kubernetes API/apimachinery/client-go v0.31.4, yaml.v3, standard context/JSON/crypto. No new dependencies or verifier image.

**Spec:** `docs/superpowers/specs/2026-10-16-pdb-repairs-design.md`, approved by the user's subsequent “continiue”. This plan awaits review before execution.

## Global Constraints

- Existing `policy/v1` PDBs protecting one same-namespace Deployment or StatefulSet with a supported scale target.
- Correct a selector only when its current selection is empty and the approved replacement selects exactly the intended controller's complete stable Pod set.
- Preserve `unhealthyPodEvictionPolicy`, metadata, owner references and every unrelated field.
- No PDB creation/deletion, overlap removal, label changes, controller edits, scale changes, node drains, Pod deletions/evictions, RBAC changes or unhealthy-Pod policy relaxation.
- No GitOps/Helm/operator-owned direct mutation. Source ownership ambiguity is a blocker.
- Require a positive minimum healthy floor and non-negative disruption ceiling. Initial implementation refuses active HPA targets and unsupported rollout modes.
- Preview and JSON/YAML diagnostic output never change cluster resources or source files. No automatic rollback and no reusable authority in serialized output.
- Preserve all provider/workload/controller/Service/source/Helm tests. Do not modify EKS, ONF or existing demo resources incidentally; preserve local infrastructure/state/demo files.
- Execute inline, then one fresh whole-branch reviewer under executing-plans; no per-task delegation. Start from `d0095c0`; keep document checkpoints, then create `codex/pdb-repairs` at execution time. Commit/push only product code/docs/tests. No merge or deployment.

## Review Focus

1. Scan label filters or a partial/denied list hide an overlapping budget or an owned Pod: repair requires complete fresh inventories (Tasks 1, 3, 6).
2. API defaults, percentage rounding or numeric-looking YAML strings change budget semantics: preserve IntOrString and compare admitted intent exactly (Tasks 2, 4).
3. A write succeeds but its response or observation is lost: report partial/uncertain completion, never retry or claim no changes (Task 5).
4. A user mutates a public plan, changes policy bytes or reuses a same-name object: private copied bindings and UID checks refuse authorization (Tasks 2, 4, 5).
5. A command outlives its timeout or rollback overwrites another actor: deadlines begin before initial reads, and inverse patches test identity/current edited fields (Tasks 4–6).

## File map and shared interfaces

Create `internal/pdbfix/{types,collect,diagnose,policy,budget,candidate,proof,verify,client,delivery,observe,rollback,context}.go`, with focused tests beside each responsibility. Analyzer changes belong in `internal/analyzer/pdb.go`, new `pdb_reader.go` and tests. CLI changes belong in new `internal/cli/pdb_fix.go`, `pdb_source.go` and tests, with minimal integration in `root.go`/`fix_prepare.go`. Source translation stays in existing repo seams unless a PDB-specific helper is required. Add `docs/guides/pdb-repairs.md`, update `docs/reference/remediation-coverage.md`, and create tagged `test/e2e/gate_pdb_test.go`, synthetic fixtures and a dedicated local-kind harness.

Canonical interfaces, defined by their producing tasks:

```go
// Task 1, pdbfix. Kubernetes aliases: corev1, policyv1, autoscalingv2.
type Target struct { Kind, Name string }
type Reader interface {
 GetPDB(context.Context, string, string) (*policyv1.PodDisruptionBudget, error)
 GetWorkload(context.Context, string, string, string) (map[string]any, error)
 ListPods(context.Context, string) ([]corev1.Pod, error)
 ListPDBs(context.Context, string) ([]policyv1.PodDisruptionBudget, error)
 ListHPAs(context.Context, string) ([]autoscalingv2.HorizontalPodAutoscaler, error)
 ResolveOwner(context.Context, corev1.Pod) (kube.WorkloadOwner, error)
}
type EvidenceIssue struct { Code, Resource, Detail string }
type Diagnosis struct { Code, Severity, Cause, Next string }
type Snapshot struct {
 PDB *policyv1.PodDisruptionBudget
 Workload map[string]any
 Pods []corev1.Pod
 Budgets []policyv1.PodDisruptionBudget
 HPAs []autoscalingv2.HorizontalPodAutoscaler
 Owners map[string]kube.WorkloadOwner // Pod UID keys
 Issues []EvidenceIssue
}
func Collect(context.Context, Reader, string, string, *Target) (Snapshot, error)
func Diagnose(Snapshot) []Diagnosis

// Task 2. Authority is private copied intent/bytes/digest.
func LoadPolicy(string) (*Authority, error)
func (*Authority) Target() Target
func (*Authority) ProposalContext(string, string) (string, error) // namespace, PDB name
type Effect struct { Replicas, RequiredHealthy, AllowedDisruptions int32 }
func EvaluateBudget(policyv1.PodDisruptionBudgetSpec, int32, int32) (Effect, error)
// arguments: budget spec, desired replicas, healthy Pods; no status inference.

// Task 3. Plan has public diagnostic fields only; private proof is omitted from JSON/YAML.
type Plan struct {
 Resource, Namespace, Status, Diff, Scope string
 ChangedFields, Prerequisites, Warnings []string
 proof *proof
}
func Prepare(context.Context, Reader, *Authority, string) (Plan, error) // optional AI patch
func (Plan) PatchYAML() (string, error)

// Task 4. Runtime cannot create/delete Pods, PDBs or evictions.
type Runtime interface {
 Reader
 PatchPDB(context.Context, string, string, []byte, bool) (*policyv1.PodDisruptionBudget, error)
} // bool is dry-run; always JSONPatch with UID/resourceVersion tests.
func NewClient(*kube.TypedClient) (Runtime, error)
func Verify(context.Context, Runtime, Plan) (*Receipt, error)

// Task 5. Receipt and Applied keep all authority private and copied.
type Outcome struct { Status, Scope, Summary string; Warnings []string }
func Apply(context.Context, Runtime, Plan, *Receipt) (*Applied, Outcome, error)
func Observe(context.Context, Runtime, Plan, *Applied) (Outcome, error)
func Restore(context.Context, Runtime, *Applied) (Outcome, error)

// Task 6, cli. Runtime injection supports complete side-effect assertions.
func runPDBFix(context.Context, io.Writer, io.Writer, options, analyzer.Finding) int
func runPDBFixRuntime(context.Context, io.Writer, io.Writer, options, pdbfix.Runtime, analyzer.Finding) int
```

Do not expose snapshots through plan serialization. Snapshot values alone are evidence, never authority; all authorization paths validate private deep-copied content. Preserve wrapped API causes, with concise redacted presentation at the CLI.

### Task 1: Complete PDB evidence and precise diagnosis

**Files:** Create pdbfix `types.go`, `collect.go`, `diagnose.go` and tests; modify analyzer `pdb.go`; create `pdb_reader.go`, `pdb_test.go`. Update the coverage document with compatible status semantics.

**Interfaces:** Produces `Target`, `Reader`, `Snapshot`, `EvidenceIssue`, `Diagnosis`, `Collect`, `Diagnose`. Analyzer's adapter uses `kube.Reader`, converting actual API maps rather than the reduced kube.Pod struct. Avoid ScanContext's selector-filtered Pod/resource caches for namespace evidence. No dependency from pdbfix to analyzer.

- [ ] **Step 1:** Write `TestPDBDiagnosisClassifications`: nil selector selects none; empty selector evaluates namespace-wide; match expressions select correctly; healthy intentional zero allowance produces informational `DisruptionProtectionActive`; insufficient health retains the legacy `DisruptionsBlocked` status with precise evidence. Cover `PDBNoSelectedPods`, `PDBStatusStale`, `PDBReconciliationFailed`, `PDBOverlap`, `PDBUnsupportedOwnership`, and `PDBEvidenceUnavailable`. Keep simultaneous findings, stable IDs and the original four count labels where valid.
- [ ] **Step 2:** Write `TestPDBInventoryIgnoresDisplayFilters` and `TestPDBReadErrorsRemainUnavailable`: an overlapping differently labeled PDB is still found; a denied Pod/PDB/HPA read is an evidence issue, never a successful empty result. Target read failure returns an error. Duplicate/malformed inventory identities refuse complete evidence. Use selected-owner UID chains and preserve unsupported-owner evidence.
- [ ] **Step 3:** Run `GOCACHE=/tmp/fixora-go-cache go test ./internal/pdbfix ./internal/analyzer -run 'TestPDB' -count=1`; confirm missing APIs or asserted behavior fail.
- [ ] **Step 4:** Implement the exact Task 1 interfaces. Deep-copy inputs; use `metav1.LabelSelectorAsSelector`; only trust current-generation status. Resolve selected owners for diagnosis and every potentially intended Pod when Target is supplied. Mark missing or failed evidence explicitly; retain unknown status counts as unknown. Fetch relevant events via the existing analyzer event path without making optional event access authorize repairs. Classify unsupported/overlapping evidence without proposing automatic policy relaxation.
- [ ] **Step 5:** Repeat Step 3, then run `go test ./internal/analyzer ./internal/fix -count=1`; require PASS. Document that new precise statuses replace generic findings, while unhealthy-budget `DisruptionsBlocked` remains compatible.
- [ ] **Step 6:** Commit only Task 1 product files as `feat: diagnose PDB protection and disruption blockers precisely`.

### Task 2: Strict availability policy and budget arithmetic

**Files:** Create pdbfix `policy.go`, `budget.go`, associated tests; add the policy schema/example to `docs/guides/pdb-repairs.md`.

**Interfaces:** Consumes Task 1 Target. Produces `Authority`, `LoadPolicy`, `Target()`, `ProposalContext`, `Effect`, `EvaluateBudget`.

Use `apiVersion: fixora.io/v1alpha1`, `kind: PDBRepairPolicy`, `metadata: {name, namespace}`, and `spec: {pdb: {name}, workload: {kind, name}, selector: <metav1.LabelSelector>, budget: {minAvailable OR maxUnavailable}, availability: {minimumHealthyReplicas, maximumConcurrentDisruptions}}`. Policy namespace is the namespace for PDB/workload. Only Deployment/StatefulSet kinds; selector must be valid and non-empty. Required availability fields use presence-aware decoding so omitted maximum is not confused with an approved zero. Budget YAML preserves integer versus percentage string semantics; numeric-looking strings without `%` are invalid.

- [ ] **Step 1:** Write `TestPDBPolicyStrictIntent`: valid integer/percentage examples load; unknown fields, duplicates at any depth, extra YAML documents, absent limits, zero healthy floor, negative ceiling, bad names/kinds, nil/empty selector, both/neither budget fields and malformed quantities fail. Input is a regular local file bounded to 1 MiB, resolved once; no URI or follow-up include loading.
- [ ] **Step 2:** Write `TestPDBBudgetRoundingAndBounds`: for three healthy replicas, `minAvailable: "50%"` yields required=2/allowed=1; `maxUnavailable: "50%"` yields required=1/allowed=2. Test one replica, zero desired (not eligible), integer values exceeding replicas, zero allowance, overflow/malformed strings and nonhealthy counts. `TestPDBAuthorityCannotBeReconstituted` rejects zero-value, mutated or JSON-roundtripped authority.
- [ ] **Step 3:** Run `go test ./internal/pdbfix -run 'TestPDBPolicy|TestPDBBudget|TestPDBAuthority' -count=1`; observe RED.
- [ ] **Step 4:** Implement the interfaces using Kubernetes-compatible IntOrString rounding (`GetScaledValueFromIntOrPercent` with round-up), checked integer ranges and private immutable input copies. `EvaluateBudget` returns effects without granting authority; clamp maxUnavailable-derived required health at zero as Kubernetes does. Candidate validation additionally requires required health no greater than desired replicas, at least the approved healthy floor, and permitted disruptions no greater than the approved ceiling. Test an impossible minAvailable above desired replicas as an explicit prerequisite, rather than a repair success. ProposalContext reveals approved target/selector/budget/bounds only after exact namespace/name matching; it excludes file paths and mutable authority.
- [ ] **Step 5:** Repeat Step 3 and require PASS; inspect serialized numeric/percentage examples for scalar YAML values, not Go struct fields.
- [ ] **Step 6:** Commit as `feat: validate independent PDB repair intent and availability bounds`.

### Task 3: Exact candidates and stable-workload eligibility

**Files:** Create pdbfix `candidate.go`, `proof.go`, associated tests; extend `collect.go` only where targeted ownership/scale evidence requires it.

**Interfaces:** Consumes Tasks 1–2, produces `Plan`, private `proof`, `Prepare`, `PatchYAML`. No analyzer.Finding dependency; the CLI binds the exact target before invoking Prepare. Plan status values: `prerequisite-required`, `review-required`, `source-review-required`, `candidate-prepared`; all have Scope `unverified`.

- [ ] **Step 1:** Write `TestPDBPrepareExactCorrections`: generated controller/Pod/label names; healthy converged Deployment via ReplicaSet and StatefulSet direct owner; empty-selection replacement; budget-only correction and atomic type-switch; no-op refusal. Assert only `/spec/selector`, `/spec/minAvailable`, `/spec/maxUnavailable` can change, and absent/null inverse information survives. PatchYAML emits valid `policy/v1` partial-manifest scalars.
- [ ] **Step 2:** Write `TestPDBPrepareRefusesUnsafeMembership`: partial current selection cannot be changed; unrelated current/desired selections, unmatched intended Pods, duplicate/replaced UIDs, incomplete owner chain, deleting/terminating/non-Ready Pods, paused/unfinished Deployment, non-RollingUpdate StatefulSet, nonzero StatefulSet partition or unequal revisions, active disruption, stale/failed PDB status, zero desired, incomplete evidence or active HPA block direct eligibility. Only fully updated default RollingUpdate StatefulSets and complete Deployments qualify.
- [ ] **Step 3:** Write `TestPDBCandidateAndIntentBindings`: caller mutates policy/Plan/returned slices without creating authority; AI patch must match approved changed fields exactly and cannot change metadata or unhealthyPodEvictionPolicy. Owned/GitOps/Helm resources retain a reviewed candidate with direct delivery blocked, not an eligible receipt. Recognize existing ownership markers and any PDB controller owner reference; ambiguity stays blocked.
- [ ] **Step 4:** Run `go test ./internal/pdbfix -run 'TestPDBPrepare|TestPDBCandidate' -count=1`; observe RED.
- [ ] **Step 5:** Implement candidate creation, stable-controller/Pod inventory checks, overlap checks, exact-bound calculations and copied proof. Treat independent lists as non-atomic; bind semantic inputs and relevant metadata rather than volatile timestamps. ResourceVersion guards the actual PDB mutation, while spec/identity/generation and selected health drift invalidate evidence. Missing intent produces prerequisites, never placeholder patches.
- [ ] **Step 6:** Repeat Step 4 and require PASS; commit as `feat: prepare policy-bound PDB corrections for stable workloads`.

### Task 4: Typed runtime and bounded preflight receipt

**Files:** Create pdbfix `client.go`, `context.go`, `verify.go` and tests; complete `proof.go` freshness checks.

**Interfaces:** Consumes Reader/Plan, produces `Runtime`, `NewClient`, `Receipt`, `Verify`. Typed workload reads cover Deployment/StatefulSet/ReplicaSet; use existing `kube.ResolveWorkloadOwner` through a typed ownership adapter. No Service-specific adapter reuse by widening its exported authority.

- [ ] **Step 1:** Write `TestPDBVerifyDryRunOnly`: typed fake reactor receives JSONPatch with UID/resourceVersion tests and `DryRunAll`; admitted candidate must preserve all unrelated spec/metadata. No create/delete/eviction calls occur. Unchanged automatic API metadata may normalize; labels, annotations, finalizers and owner references remain bound. Refuse substantive admission rewrites.
- [ ] **Step 2:** Write `TestPDBVerifyFreshnessAndDeadline`: an initial blocked read exits at parent deadline; no-parent library operations default to three minutes before their first read. Earlier parent deadlines are never extended. Drift in Pods, health, ownership, overlap, HPA inventory or desired replicas between dry-run and recollection blocks the receipt. Forbidden/timeout errors preserve causes. `TestPDBReceiptCannotReplayOrSerialize` refuses tampered Plan, replaced UID, zero/roundtripped receipt and reuse after consumption.
- [ ] **Step 3:** Run `go test ./internal/pdbfix -run 'TestPDBVerify|TestPDBReceipt' -count=1`; observe RED.
- [ ] **Step 4:** Implement runtime typed list/get/patch operations and verification: validate private proof, recollect, compare, dry-run exact mutation, compare admitted intent, recollect again, then seal private receipt. Bind candidate/policy/evidence; receipt consumption must be concurrency-safe and single-attempt. Never create temporary PDBs or send even dry-run evictions. Library operation defaults do not reset an existing caller deadline.
- [ ] **Step 5:** Repeat Step 3 and run `go test -race ./internal/pdbfix -count=1`; require PASS. Commit as `feat: verify PDB candidates without disrupting live Pods`.

### Task 5: Guarded apply, truthful observation and explicit inverse

**Files:** Create pdbfix `delivery.go`, `observe.go`, `rollback.go`, associated tests.

**Interfaces:** Consumes Runtime/Receipt/Plan, produces private `Applied`, `Outcome`, `Apply`, `Observe`, `Restore`. Outcome statuses: `applied`, `policy-converged`, `unchanged`, `outcome-uncertain`, `conflict`, `observation-failed`, `restored`, `restore-observation-failed`. Any non-success carries error and accurate observed state. Applied is non-nil only when exact identity/edited intent is confirmed; an uncertain write never grants rollback authority.

- [ ] **Step 1:** Write `TestPDBApplyAndObserve`: one exact guarded JSONPatch; selector replacement removes old keys; budget switch removes opposite field atomically. Recheck evidence and ownership immediately before write, validate response, then require observedGeneration=current generation, computed expected/current/desired/allowed status agreement, usable condition, unchanged stable membership/health/scale and no overlap before `policy-converged`. Poll with context-aware bounded waits; no claim of eviction success.
- [ ] **Step 2:** Write `TestPDBLostResponseAndPartialOutcomes`: server applied but response lost; read shows exact candidate => report candidate observed with uncertain authorship, not a clean confirmed write; unchanged/conflicting/unreadable read outcomes remain explicit. No retry or automatic inverse. Confirmed write followed by timeout/read denial/health loss reports partial completion, not no changes. Status-only API updates must not spuriously invalidate semantic evidence.
- [ ] **Step 3:** Write `TestPDBRestoreFieldGuards`: original UID plus current edited-field tests and current resourceVersion; restore original absence/null and preserve unrelated concurrent fields. Changed edited fields, ownership change, replacement UID or altered response refuse success. Explicit restoration of an originally misconfigured budget requires the CLI to display that original policy is being restored; no claim it satisfies the approved repair floor. Validate restored controller status separately.
- [ ] **Step 4:** Run `go test ./internal/pdbfix -run 'TestPDBApply|TestPDBLost|TestPDBRestore' -count=1`; observe RED.
- [ ] **Step 5:** Implement the interfaces using private single-use receipt, copied original/candidate, exact JSON field states, independent current read before inverse, and full bound spec/metadata response checks. Preserve timeout from initial read across apply and observe when called under one CLI operation. An explicit later rollback may get a fresh bounded context; it remains a distinct consented operation.
- [ ] **Step 6:** Repeat Step 4 and run all pdbfix tests with race; require PASS. Commit as `feat: apply and observe PDB repairs with guarded rollback`.

### Task 6: CLI, AI evidence and reviewed source delivery

**Files:** Create CLI `pdb_fix.go`, `pdb_source.go`, their tests; modify `root.go`, `fix_prepare.go`, and focused existing help/option tests. Extend `docs/guides/pdb-repairs.md`.

**Interfaces:** Produces `runPDBFix`, `runPDBFixRuntime`, plus `withPDBProposalIntent(analyzer.Finding, string) (analyzer.Finding, error)`. Add `options.pdbPolicy` and `--pdb-policy <local-file>`; reject mixing Service/workload policies/baselines or `--edit-patch` and reject non-PDB targets. Route every exact PDB fix, even without policy, through the concise path. Preserve the generic pdb strategy as review-only everywhere else; never reuse workload shadow authority.

- [ ] **Step 1:** Write `TestPDBCLIOutputAndConsent`: concise cause/next/prerequisite without policy; --proof expands evidence; preview and JSON/YAML perform zero mutation/source writes, including when --apply/--delivery=pr is also supplied. No-policy AI still shows its concise assessment. Structured output is valid and contains no receipt. Unrelated existing resource routes stay unchanged. Display-only scan selectors cannot constrain repair evidence.
- [ ] **Step 2:** Write `TestPDBCLIProposalIsolation`: policy context added before existing provider augmentation and PDB path returns before generic infer/shadow/controller gates. Doctor/protocol/provider code is unchanged. AI cannot grant authority or alter fields beyond intent; deterministic mode generates the same approved candidate. Reject invalid flags before mutation; no guessed intent after provider error.
- [ ] **Step 3:** Write `TestPDBCLIDeliveryModes`: default `patch` prints reviewed candidate without production write; `cluster`/--apply needs explicit --yes or interactive consent; preflight status is not shadow verified. Bound full PDB prepare/verify/apply/observe phase to --timeout, respecting parent (analysis time counts when the same deadline applies). Ignore no timeouts silently; reject invalid values. Offer explicit rollback only with a confirmed Applied handle and fresh consent; --yes never implies automatic rollback after failure.
- [ ] **Step 4:** Write `TestPDBSourceReviewIsolation`: raw/Kustomize reviewed patch retains ownership blockers, exact file scope and clean-worktree/index checks before a requested PR. Helm translation stays manual; no workload render certificate. Reuse `repo.PreviewSourcePatch`/existing reviewed source flow only if it preserves these properties; otherwise return concrete manual guidance with no source write. Preview never creates PRs; runtime receipt cannot authorize source writes. Exercise dirty same-file changes and files changed between preview/write. Do not create a real external PR during development.
- [ ] **Step 5:** Run `go test ./internal/cli ./internal/repo -run 'TestPDB' -count=1`; observe RED.
- [ ] **Step 6:** Implement CLI interfaces and routing, source adapter, help and guide examples. Name stages `preflight passed` and `policy converged`; never `shadow verified` or `drain verified`. Output distinguishes patch/source-only work, confirmed writes, uncertain outcomes and current health, with one next action. Reuse existing redaction/source checks, extracting a shared clean-repo helper only if needed without behavior changes to Services.
- [ ] **Step 7:** Repeat Step 5, then full CLI/repo/provider/workload regression packages; require PASS. Commit as `feat: expose dynamic PDB repairs through concise CLI and source review`.

### Task 7: Local controller acceptance, full regression and durable handoff

**Files:** Create tagged `test/e2e/gate_pdb_test.go`, `test/e2e/fixtures/pdb-repair/{deployment,statefulset,overlap}.yaml`, `hack/e2e-pdb.sh`; finalize guide/coverage and `docs/superpowers/reports/2026-10-16-pdb-repairs.md`.

**Interfaces:** Consumes the complete public pdbfix/CLI APIs. E2E uses tags `e2e,e2e_delivery` and `FIXORA_E2E_PDB=1`; the harness creates a dedicated disposable local kind context and unique namespaces. Refuse non-kind/shared contexts before writes; never use the user's current EKS context by default. No verifier image build/publish needed. Fixture image is configurable for local availability.

- [ ] **Step 1:** Write `TestPDBControllerContractsAndRollback`: run Deployment and StatefulSet cases with three healthy replicas; approved empty-selector-match correction and budget type switch; observe actual current-generation status and verify numeric/percentage rounding. Record original/after Pod UIDs and assert none were evicted/recreated. Exercise overlap and active-HPA refusal, preview side effects, and explicit inverse. The fixture's broken selector must be a nonmatching label selector, never `{}` selecting every namespace Pod.
- [ ] **Step 2:** Compile with `GOCACHE=/tmp/fixora-go-cache go test -c -tags='e2e,e2e_delivery' -o /tmp/fixora-pdb-e2e.test ./test/e2e`. If APIs are complete and compile passes, keep E2E failing assertions for the local controller run rather than introducing intentional compile breakage. Implement the dedicated harness and fixtures, checking kind/docker availability before cluster creation.
- [ ] **Step 3:** Run `bash -n hack/e2e-pdb.sh`; run the dedicated harness only when Docker/kind are available. If unavailable, report skipped live controller validation explicitly; do not substitute ONF/EKS. Cleanup targets only the dedicated local cluster/namespace owned by this run.
- [ ] **Step 4:** Run fresh `GOCACHE=/tmp/fixora-go-cache go test -count=1 ./...`, `go test -race -count=1 ./...`, `go vet ./...`, `make build`, tagged E2E compile and `git diff --check`. Confirm each exit status; network-listener tests may need the standard reviewed sandbox escalation. Inspect the changed-file list for infra/state/demo leakage.
- [ ] **Step 5:** Use the executing-plans final whole-branch review once, against the execution branch base, with approved spec/plan and actual checks. Reproduce actionable findings, add regression cases, fix and re-run affected checks; broaden testing only if justified. No per-task subagent dispatch. Preserve the user's native execution choice.
- [ ] **Step 6:** Commit/push product changes to `codex/pdb-repairs` only after checks and review fixes. Record exact commit, commands/results, skipped checks, limits and next HPA design slice in the durable report. Update coverage truthfully; do not mark all of Step 5 complete. Final response links the branch/report and clearly states any unexecuted local/live tests.

## Plan self-review and handoff

Coverage checked: diagnosis/evidence (Task 1), intent/arithmetic (Task 2), candidate scope/ownership (Task 3), preflight/authority/deadlines (Task 4), apply/partial outcomes/rollback (Task 5), AI/CLI/source/output (Task 6), acceptance/regression/durable continuation (Task 7). Every Review Focus item has named tests above. Interface ownership is defined once; pdbfix never imports analyzer. No implementation or cluster mutation is part of this planning checkpoint.

Execution method remains native. After the user reviews this plan, invoke executing-plans and using-git-worktrees as required by that workflow; preserve existing unrelated local changes and avoid copying local infrastructure material into the implementation checkout.
