# Security Verification Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans. Steps use checkbox (`- [ ]`) syntax.

**Goal:** Repair proved admission/security failures through bounded strengthening without changing application identity or relaxing controls.
**Architecture:** Link current admission/runtime failures to a precise effective setting and current revision, validate same-identity strengthening, then verify admission and runtime behavior in shadow and live Pods. Existing cache repair stays independent.
**Tech Stack:** Go, kube/YAML/shadow/ops packages and existing namespace/event readers.
**Spec:** `docs/superpowers/specs/2026-10-05-verification-policies-design.md`, section 3c.

## Global Constraints

Root plan constraints/interfaces apply. “Explicit hygiene recommendations remain review-oriented rather than being presented as repaired incidents.” Do not bypass privileged/capability/Secret/PVC/host/service-account source restrictions. No UID/GID guesses, capability additions, Unconfined/Localhost profiles, Windows semantics, persistent ownership or readOnlyRootFilesystem mutations.

## Review Focus

Pin current versus historical FailedCreate, old healthy Pods with a denied latest revision, conflicting Pod/container effective UID, namespace policy changes and admission-only false recovery in H1/H2.

## Task H1: causal admission diagnosis and strengthening validator

**Files:** Create internal/analyzer/admission_evidence.go/tests, internal/repairpolicy/security.go/tests; modify kube/types.go to preserve Event involved-object UID/API version, analyzer/analyzer.go/pod_security.go only for relevant binding, CLI repair_baseline.go/ai_repair.go, and inventory entries if named findings change.
**Consumes:** root Snapshot/Authority/Proof and the shared CLI baseline options from P1.
**Produces:** `func Security(snapshot Snapshot, baseline *Authority, patch string) (*Proof, error)` in repairpolicy. Add policy strategy `security-strengthen`, distinct from filesystem and source-review security.

- [ ] Write `TestAdmissionFailureLinkedToCurrentOwnedRevision`, `TestOldHealthyPodDoesNotHideCurrentAdmissionFailure`, `TestHistoricalAdmissionEventCannotOverrideHealthyCurrentRevision`, `TestSecurityEffectivePositiveUIDAndExactApprovedStrengthening` and `TestSecurityRefusesRelaxationIdentityGuessAndUnprovedPolicy`.
- [ ] Run `GOCACHE=/tmp/fixora-go-cache go test ./internal/analyzer ./internal/repairpolicy ./internal/cli -run 'Admission|Security|RepairBaseline'`; confirm red.
- [ ] Preserve event UID; bind FailedCreate/admission errors to the controlling owner/current desired template/revision and namespace enforcement. Do not globally turn PodSecurity hygiene findings into incidents. Permit runAsNonRoot=true only with an existing effective explicit positive UID and causal proof. For allowPrivilegeEscalation=false, seccomp RuntimeDefault and drop ALL, require exact approved baseline leaves plus enforced Pod Security Admission field evidence. Preserve existing effective UID/GID and every unrelated field.
- [ ] Extend field classification to the dedicated family; retain concrete review patches when baseline/policy/proof is absent. Reuse current cache behavior unchanged; refuse unsupported policy versions/OS/fields.
- [ ] Run security/admission tests, cache/demo/controller regressions and coverage inventory; require PASS.
- [ ] Commit `feat: diagnose and bind security strengthening repairs`.

## Task H2: admitted settings plus runtime verification

**Files:** Create internal/shadow/security_validation.go/tests, internal/ops/security.go/tests; modify shadow candidate/validation/run/types and CLI policy delivery wiring.
**Produces:** `func VerifySecurity(ctx context.Context, r kube.Reader, proof *Proof, patched *kube.Pod, patch string) (*Receipt, error)` in repairpolicy/security.go; `func VerifySecurityRollout(ctx context.Context, r kube.Reader, f analyzer.Finding, p fix.Plan) (RolloutOutcome, error)` in ops/security.go.

- [ ] Write `TestSecurityAdmissionAcceptsButRuntimeFailureRejectsReceipt`, `TestSecurityAdmittedSettingsAndApplicationIdentityMatch`, `TestSecurityPolicyChangesBeforeDelivery`, `TestOriginalPrivilegedOrPersistentSourceStaysBlocked` and target outcome matrix tests.
- [ ] Run focused tests to confirm failure.
- [ ] Require the matching bound proof before clone creation; use same-namespace server dry-run and patched shadow admission; keep original-source safety checks. Require admitted effective settings, unchanged identity, existing readiness/soak, no original failure/restart signal and exact candidate binding. Revalidate current namespace/target proof before guarded apply; inspect admitted live updated Pods after rollout. Keep incomplete/unknown/future-run observations explicit.
- [ ] Exercise low-confidence/recovered/edited/replayed/bypass refusals, source-managed delivery and shadow cleanup. Run existing security/filesystem/AI regressions; require PASS.
- [ ] Commit `feat: verify security repairs through admission and runtime`.
