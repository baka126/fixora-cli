# Trusted Startup Restoration Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans. Steps use checkbox (`- [ ]`) syntax.

**Goal:** Extend explicit, proof-bound startup restoration across workload envelopes and uniquely failing regular containers.
**Architecture:** Read operator authority with the shared core, select one proven failing container, generate only command/args changes, and bind the exact patch through shadow and delivery. Controller policies still determine direct versus source-review delivery.
**Tech Stack:** existing Go/kube/YAML/shadow packages; no dependencies.
**Spec:** `docs/superpowers/specs/2026-10-05-verification-policies-design.md`, section 3a.

## Global Constraints

All constraints and shared interfaces in `2026-10-05-verification-policies.md` apply. “Only the selected container's command/args change.” “AI may explain why restoration is appropriate and suggest evidence to locate an intended command. It cannot invent a server/sleep command, turn exit 1 into a keepalive, or revise the operator's approved startup patch.” No startup-image history inference.

## Review Focus

Pin sidecar/init/multiple-failure selection, baseline-image mismatch, mutable-tag provenance caveat, empty/omitted args semantics, and edited/replayed startup receipts in S1/S2 tests.

## Task S1: baseline selection and generated patch

**Files:** Create internal/repairpolicy/startup.go and startup_test.go; modify internal/cli/startup_baseline.go/startup_baseline_test.go, internal/shadow/startup_validation.go and its tests.
**Consumes:** Snapshot, Authority, Proof, Collect, ReadBaseline and shared kube workload/owner helpers.
**Produces:** `func Startup(snapshot Snapshot, baseline *Authority, container string) (string, *Proof, error)` in repairpolicy; extended existing `withStartupBaseline` signature remains unchanged.

- [ ] Write `TestStartupSevenEnvelopesAndUniqueRegularContainer`, `TestStartupRefusesWrongImageStalePodAndAmbiguousFailure`, `TestStartupPreservesOtherContainersAndFields` and `TestStartupArgsRestorationIsExact`. Assert one selected current failed regular container, matching kind/name/ns/image, no init-container substitution, exact command/args including removal of obsolete args, no mutation of sidecars/env/resources/probes/security/storage.
- [ ] Run `GOCACHE=/tmp/fixora-go-cache go test ./internal/repairpolicy ./internal/cli ./internal/shadow -run 'Startup|DemoStartup|DemoUnknownStartup'`; verify red behavior.
- [ ] Implement Startup, using existing crash observations and explicit-baseline authority. Select from failing-container evidence; permit automatic selection only when exactly one regular failing container exists. Generate kube.WorkloadPatch; no guessed values. Update fixtures to real Pod/owner UIDs where existing mocks omit them, preserving expected behavior.
- [ ] Run the same tests plus `go test ./internal/infer ./internal/fix`; require PASS and original Deployment regressions.
- [ ] Commit `feat: extend trusted startup restoration across workloads`.

## Task S2: pinned verification and controller delivery

**Files:** Modify internal/cli/root.go, ai_repair.go, internal/shadow/run.go/types.go/validation.go, internal/ops/controller.go; add internal/cli/startup_policy_test.go and internal/shadow/startup_policy_test.go.
**Produces:** `func VerifyStartup(ctx context.Context, r kube.Reader, proof *Proof, patched *kube.Pod, patch string) (*Receipt, error)` in repairpolicy/startup.go; it requires the existing shadow readiness/soak/completion observation to succeed before invocation.

- [ ] Write `TestStartupReceiptPinsPatchAuthorityAndUID`, `TestStartupEditOrAIRetryCannotChangeBaseline`, `TestStartupDeliveryControllerMatrix` and `TestStartupSavedPlanRequiresFreshProof` and `TestUntrustedShadowRequestCannotExecuteStartupMutation`. Direct: eligible Deployment/StatefulSet/DaemonSet; CronJob future-only; Job/ReplicaSet/Pod source review. Refuse missing receipt, failed shadow, recovered Pod, changed source/identity and quick/force bypass.
- [ ] Run focused startup policy tests to confirm failure before wiring.
- [ ] Require the bound proof before any new-policy shadow resource is created, including direct/MCP request paths. Bind the Plan proof; preserve AI bypass for trusted startup and zero retries. Verify the observed clone's selected command/args equals the approved patch and source guards remain intact. Check the receipt against the exact outgoing patch and refreshed target before guarded apply. Do not make readiness alone a reusable receipt.
- [ ] Run startup tests, controller matrix and demo/provider-independent regressions; require PASS.
- [ ] Commit `feat: require pinned startup proof through delivery`.
