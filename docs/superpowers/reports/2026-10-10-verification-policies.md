# Step 3 verification-policy checkpoint

Branch: `codex/verification-policies`. Spec: `docs/superpowers/specs/2026-10-05-verification-policies-design.md`.

## Implemented

- Opaque runtime authority, live workload/Pod and candidate bindings, shadow receipts, and replay refusal.
- Exact approved startup restoration with current failing-container proof and no AI command revisions.
- Approved placement correction with current scheduler predicates, image/platform checks, node safety and capacity checks.
- Approved security strengthening with enforced PSA and current revision-linked admission failures, preserving application identity.
- Coordinated independently verified Deployment/StatefulSet/DaemonSet candidates through the single-fix analysis, inference and AI preparation path. Every shadow and guarded preflight passes before concrete patch review and apply confirmation. Each execution rechecks current identity, configuration and source ownership.
- Field-scoped reverse rollback under separate consent, UID and current resourceVersion, with readback and conflict refusal. Restore the original fields, not an entire old manifest; do not call the original failing configuration healthy.
- Distinct cancelled, not-applied, apply-unconfirmed, applied-unverified, live-verified and restored-field outcomes. Unknown live verification stops further mutation. A lost apply response does not establish whether that step mutated.

## Review findings and regression evidence

A fresh reviewer identified three Important defects. Each was reproduced before correction:

1. Placement accepted an explicit mismatching baseline image: `TestPlacementRejectsMismatchedBaselineImage` RED → GREEN; both candidate generation and direct family validation now refuse it.
2. Shadow receipt matching rejected canonical Kubernetes resource quantities: `TestCommonWitnessAcceptsCanonicalResourceQuantity` RED → GREEN; rollback/readback also covers equivalent versus changed quantities.
3. Newly introduced Helm ownership escaped live preflight: `TestPreflightRejectsNewHelmOwnership` RED → GREEN; rollback also refuses new source ownership.

Additional implementation review reproduced and corrected:

- Invalid empty valueFrom after env restoration: `TestRollbackRemovesNewValueFromAndRefusesConcurrentNestedEdits` RED → GREEN.
- Deleting concurrent entries from a newly added named list: `TestRollbackAddedListPreservesConcurrentSibling` RED → GREEN.
- Cancelled confirmation continuing to apply: `TestCoordinateCancellationAtConfirmationMutatesNothing` RED → GREEN.
- Apply-error output falsely implying no mutation: `TestCoordinateFailedApplyReportsUnconfirmedOutcome` RED → GREEN.
- Declared controller ownership cycles reaching shadows: `TestCoordinateRejectsControllerDependencyCycleBeforeShadows` RED → GREEN.
- `coordinate --preview` bypassing verification/delivery: `TestCoordinatePreviewDoesNotInitializeShadowOrDeliver` RED → GREEN.
- Concrete certified proposal review and redaction: `TestCoordinateProposalShowsCertifiedPatchAndRedactsSecrets` RED → GREEN.

## Scope boundaries

No workload names, images, commands, ports or node labels are embedded in decisions. No provider transport or model changes. Existing common repair and provider regressions remain in the suite.

Explicit controller order is preserved. Derived relationships use actual ConfigMap/Secret/PVC references and Service selectors; those dependency objects are not automatically repaired by this controller-only checkpoint. Declared ownership dependencies/cycles require source review. All shadows must succeed against current dependencies, before any candidate is applied; there is no combined future dependency sandbox.

CronJob changes remain future-run pending; Job/ReplicaSet/unsupported Pod mutations remain review. Secret/PVC/host/privilege/source-management safeguards remain. No universal fleet simulation, arbitrary command recovery, identity guesses, Helm values translation or automatic batch replay is claimed.

## Verification

All final checks passed on 2026-10-10:

- `GOCACHE=/tmp/fixora-go-cache go test -count=1 ./...`
- `GOCACHE=/tmp/fixora-go-cache go test -race -count=1 ./...`
- `GOCACHE=/tmp/fixora-go-cache go vet ./...`
- `GOCACHE=/tmp/fixora-go-cache make build`
- `GOCACHE=/tmp/fixora-go-cache go test -c -tags='e2e,e2e_delivery' -o /tmp/fixora-policy-e2e.test ./test/e2e`
- `git diff --check`

E2E compilation is not live cluster execution. This checkpoint did not apply repairs to EKS or reset its demo workloads. Local infrastructure, state, credentials and private demo/testing files are excluded from the code commits.
