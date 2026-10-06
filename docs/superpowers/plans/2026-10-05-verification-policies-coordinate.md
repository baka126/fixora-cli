# Verified Coordinated Repairs Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans. Steps use checkbox (`- [ ]`) syntax.

**Goal:** Coordinate independently verified controller repairs with exact guarded apply and conflict-safe field-scoped rollback.
**Architecture:** Share single-resource candidate preparation, certify each selected patch in shadow before any production mutation, then run an explicit apply/verify saga. Use a dedicated verified runner so old serialized flags and weaker legacy verification cannot authorize new policies.
**Tech Stack:** existing Go CLI/coordinate/ops/shadow/kube packages; no dependencies.
**Spec:** `docs/superpowers/specs/2026-10-05-verification-policies-design.md`, section 3d.

## Global Constraints

Root plan constraints/interfaces apply. “Missing/failed verification aborts the set with zero production mutations.” “Unknown/skipped/pending is unverified, not healthy.” “Never claim a Kubernetes transaction or atomic rollback.” Explicit rollback consent remains required. Initially only eligible independent Deployment/StatefulSet/DaemonSet repairs; no coordinated batch/future-only/ReplicaSet/Pod replay semantics or combined dependency shadow environment.

## Review Focus

Pin duplicate/conflicting targets, multiple-step apply failures, dependency cycles/ambiguous edges, unrelated concurrent edits and rollback refusal/replacement in C1/C2. Existing field-family safeguards apply to every step.

## Task C1: shared preparation and all-step certification

**Files:** Create internal/cli/fix_prepare.go/tests and coordinate_verified.go/tests, internal/coordinate/verified.go/tests; modify CLI root.go/coordinate.go/coordinate_derive.go; add common-candidate receipt support in repairpolicy/proof.go and shadow/run.go.
**Consumes:** all family proofs/receipts, existing common candidate validators, kube ownership, ops.TargetSnapshot and shadow.Run.
**Produces:**
- `func prepareFixPlan(ctx context.Context, reader kube.Reader, k kube.Kubectl, opts options, resource string, stderr io.Writer) (analyzer.Finding, fix.Plan, options, error)` in CLI; extract the current fix path without changing preparation order/provider behavior.
- `type CertifiedStep struct { Step Step; Proof *repairpolicy.Proof; Receipt *repairpolicy.Receipt; InversePatch string }` in coordinate; trusted fields are runtime-only (`json:"-"`).
- `func certifyCoordinateSteps(ctx context.Context, reader kube.Reader, client *kube.TypedClient, k kube.Kubectl, opts options, steps []coordinate.Step) ([]coordinate.CertifiedStep, error)` in CLI.

- [ ] Write `TestSingleAndCoordinateUseSameDynamicCandidatePreparation`, `TestCoordinateAllShadowsPassBeforeFirstApply`, `TestCoordinateFailedShadowCleansUpAndMutatesNothing`, `TestCoordinateRejectsDuplicateConflictCycleAndFutureOnlyStep`, and `TestSerializedCertifiedStepCannotAuthorizeApply`.
- [ ] Run `GOCACHE=/tmp/fixora-go-cache go test ./internal/cli ./internal/coordinate ./internal/repairpolicy -run 'Coordinate|Certified|PrepareFix'`; confirm red.
- [ ] Extract shared preparation, including deterministic evidence collection/inference and AI selection when requested, plus baseline/policy application and target gates. Use root BindCommon/VerifyCommon with the existing candidate validator for common-family certification; do not widen their allowlists. Certify all shadows and exact final patches, preserve explicit ordering, allow derived edges only from real Kubernetes references, and reject dependencies requiring another not-yet-applied candidate. Prepare all field inverses and required guarded dry-runs before the existing apply confirmation. Close temporary artifacts on every failure/cancel.
- [ ] Run existing fix/provider/demo/controller/coordinate derivation tests and new certification tests; require PASS.
- [ ] Commit `feat: certify coordinated repairs with the single-fix pipeline`.

## Task C2: verified saga and field-scoped rollback

**Files:** Create internal/coordinate/verified_rollback.go/tests and internal/cli/coordinate_verified_deps.go/tests; modify verified.go, coordinate reports/output and reference docs. Legacy raw-manifest restore is not used by the verified path.
**Produces:**
- `type VerifiedDeps interface { Preflight(context.Context, CertifiedStep) (ops.TargetSnapshot, error); Apply(context.Context, CertifiedStep, ops.TargetSnapshot) error; Verify(context.Context, CertifiedStep) (ops.RolloutOutcome, error); RestoreFields(context.Context, CertifiedStep) (ops.RolloutOutcome, error) }` in coordinate.
- `func RunVerified(ctx context.Context, steps []CertifiedStep, d VerifiedDeps, confirmApply, confirmRollback func() bool) Report` in coordinate.
- `func BuildInversePatch(before map[string]any, verifiedPatch string) (string, error)` in coordinate/verified_rollback.go.

- [ ] Write `TestVerifiedSagaStopsOnUnknownAndReportsExactMutations`, `TestSecondStepFailureOffersReverseFieldRollback`, `TestFieldRollbackPreservesUnrelatedChangesAndDeletesAddedFields`, `TestRollbackRefusesChangedTouchedFieldsOrReplacement`, `TestRollbackConsentAndPartialFailureAreTruthful`, and transient failure/cancel tests.
- [ ] Run focused tests and confirm red.
- [ ] Implement guarded per-step apply using the exact live preflight snapshot; refuse stale proof/candidate/source ownership at execution. Accept only explicit live success classes, not legacy rolloutHealthy's unknown/skipped semantics. Stop on apply/verification failure and report the actual applied prefix. Build inverse only for changed fields, with container/env/list identity and null/deletion semantics; before restore, require same target UID and unchanged Fixora-applied values in those fields, then use a fresh version precondition. Preserve unrelated edits, restore in reverse order only after consent, and verify/report each restore. A conflict is a refusal, not a successful rollback.
- [ ] Test all fault cases and existing CLI confirmation behavior; require PASS. Update concise text and structured reports without calling pending results healthy.
- [ ] Commit `feat: guard coordinated delivery and field-scoped rollback`.
