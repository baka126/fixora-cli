# Placement Verification Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans. Steps use checkbox (`- [ ]`) syntax.

**Goal:** Verify approved selector/required-node-affinity repairs and exact missing NoSchedule tolerations dynamically.
**Architecture:** Classify current placement failures separately from existing resource-capacity repairs, bind operator-approved placement fields, then prove scheduler-selected placement in shadow and live updated Pods. Unsupported placement semantics remain concrete source review.
**Tech Stack:** Go, kube Reader/TypedClient, existing image inspection/resource.Quantity/YAML packages.
**Spec:** `docs/superpowers/specs/2026-10-05-verification-policies-design.md`, section 3b.

## Global Constraints

Root plan constraints/interfaces apply. “Without an intent baseline, provide the candidate and the specific missing proof for source review.” Preserve tolerations and unrelated scheduling fields. No node mutation, wildcard Exists/NoExecute/control-plane/pressure bypass, nodeName/schedulerName/priority/preemption changes or automatic DaemonSet placement delivery.

## Review Focus

Pin source nodeName/custom scheduler/pod affinity/topology spread, sanitizer-induced selector differences, outdated labels/taints, platform mismatch and resource/volume blockers in P1/P2 refusals.

## Task P1: causal placement and authority validator

**Files:** Create internal/repairpolicy/placement.go and placement_test.go, internal/cli/repair_baseline.go and repair_baseline_test.go; modify internal/cli/root.go/options/help wiring, ai_repair.go, internal/fix/controller_policy.go; add internal/analyzer/placement_evidence.go and tests only for current predicate diagnosis.
**Consumes:** root Snapshot/Authority/Proof; kube.GetNodes/GetEvents and image inspection.
**Produces:** `func Placement(ctx context.Context, snapshot Snapshot, baseline *Authority, patch string, inspect func(context.Context, string) (image.Result, error)) (*Proof, error)`; `func withRepairBaseline(ctx context.Context, reader kube.Reader, opts options, f analyzer.Finding, p fix.Plan) (fix.Plan, error)` in CLI. Add options `repairBaseline`, `repairPolicy` and flags `--repair-baseline`, `--repair-policy placement|security`; require selection only for ambiguous baseline families.

- [ ] Write `TestPlacementApprovedSelectorAffinityAndExactToleration`, `TestPlacementCannotBroadenUnapprovedIntent`, `TestPlacementUnsupportedRemainingConstraintsRefuse`, `TestPlacementRequiresCurrentMatchingSchedulerEvidence` and baseline/help tests. Use varied pool/label/taint values, node sets and images. Assert immutable/source-review kind outcomes.
- [ ] Run `GOCACHE=/tmp/fixora-go-cache go test ./internal/repairpolicy ./internal/cli ./internal/analyzer -run 'Placement|RepairBaseline'`; confirm red.
- [ ] Implement strategy `restore-placement`. Validate exact nonempty approved nodeSelector, bounded required nodeAffinity label predicates, exact Equal/key/value/NoSchedule additions and unchanged existing tolerations. Require Pending/Unschedulable causal evidence linked to current Pod/owner UID and the changed predicate; no historical/message-only identity binding. Read nodes and verify compatible Ready/schedulable candidates. Refuse unreadable or unsupported constraints, unresolved volume/quota/capacity blockers and image-platform mismatch. Baseline field absence never authorizes deletion.
- [ ] Store the loaded authority as the runtime-only Plan.PolicyAuthority and any explicit family as PolicySelection; AI selection evaluates its candidate against that same authority/selection rather than opening model-supplied paths. Retain source-review AI proposals when authority is absent; keep existing capacity inferrer behavior. Route only singular field families; mixed changes remain review.
- [ ] Run placement tests and original pending/image/demo/controller regression packages; require PASS.
- [ ] Commit `feat: add proof-bound placement candidates`.

## Task P2: scheduler witness, revalidation and delivery

**Files:** Create internal/shadow/placement_validation.go/tests, internal/ops/placement.go/tests; modify shadow candidate/validation/clone/run/types and CLI delivery/preflight wiring.
**Produces:** `func VerifyPlacement(ctx context.Context, r kube.Reader, proof *Proof, patched *kube.Pod, patch string) (*Receipt, error)` in repairpolicy/placement.go and `func VerifyPlacementRollout(ctx context.Context, r kube.Reader, f analyzer.Finding, p fix.Plan) (RolloutOutcome, error)` in ops/placement.go. Receipts require the existing readiness/soak result first.

- [ ] Write `TestPlacementShadowBindsEligibleSchedulerSelectedNode`, `TestPlacementSourceIsolationDoesNotFakeSuccess`, `TestPlacementLabelsTaintsOrTemplateChangedBeforeApply`, `TestPlacementLiveReplicasRemainUnverifiedOutsideApprovedPool` and target matrix tests.
- [ ] Run focused tests and confirm failure.
- [ ] Add policy-aware candidate validation; require its bound proof before clone creation and keep generic strategies' scheduling denylist. Preserve approved predicates in clone; never force nodeName. Bind receipt to an actual scheduler-assigned eligible node and observed Ready/soak success; check current node identity/labels/taints and source constraints. Refresh proof before exact guarded apply. After rollout, inspect updated owned live Pods for approved placement. DaemonSet placement remains review; CronJob acceptance remains future-run pending, not verified scheduling recovery.
- [ ] Test missing/forged/replayed receipt, edited candidate, failed scheduling/soak and bypass flags. Require PASS with existing shadow source/cleanup regressions.
- [ ] Commit `feat: verify placement before and after workload delivery`.
