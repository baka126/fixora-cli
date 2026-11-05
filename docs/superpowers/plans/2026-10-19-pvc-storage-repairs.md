# PVC and Storage Repairs Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add policy-bound, identity-guarded PVC class assignment and capacity expansion with evidence-rich diagnosis, safe verification, and clear CLI behavior.

**Architecture:** Add `internal/storagefix` as an isolated policy, candidate, verify, delivery, observe subsystem using typed Kubernetes APIs. Improve analyzer evidence separately; route `fix` through an explicit `--pvc-policy` path. Model expansion as non-rollbackable and fail closed for data-bearing or source-managed operations.

**Tech Stack:** Go, Kubernetes `client-go`, typed core/storage APIs, fake clientsets, existing CLI/output/analyzer patterns.

**Spec:** `docs/superpowers/specs/2026-10-19-pvc-storage-repairs-design.md`

## Global Constraints

- Preview performs no writes or server-side dry-run.
- Mutation authority comes only from a strict local exact-match policy; AI output is never authority.
- Every mutating request checks exact PVC UID/resourceVersion and only changes `spec.storageClassName` or `spec.resources.requests.storage` as approved.
- Expansion is monotonic and non-rollbackable; report this before apply.
- No shadow verifier mounts a production PVC.
- Missing/read-denied evidence is unknown, never proof of absence.
- Preserve unrelated dirty files and existing repair behavior.

## Review Focus

- PVC replacement or concurrent edit after prepare: a UID/resourceVersion mismatch prevents mutation.
- StorageClass is missing, denied, default-only, or does not allow expansion: no candidate is authorized.
- Admission rewrites another PVC field or the approved value: reject as modified candidate.
- Resize request is equal/smaller than request/capacity or exceeds policy maximum: reject.
- StatefulSet/GitOps/Helm ownership, multiple consumers, or unclear topology: remain review-only.

---

### Task 1: Policy schema and PVC repair candidates

**Files:** Create `internal/storagefix/policy.go`, `types.go`, `candidate.go`; tests `policy_test.go`, `candidate_test.go`.

**Interfaces:** `LoadPolicy(path string) (*Authority, error)` parses one strict local `PVCRepairPolicy`; `Prepare(ctx context.Context, r Reader, a *Authority, f analyzer.Finding) (Plan, error)` yields exact PVC identity and one approved field change or a refusal.

- [x] Test strict YAML schema, duplicate keys, symlinks/special files, size cap, exact namespace/PVC policy identity, exact existing class, bounded maximum size, and policy-digest recheck.
- [x] Run `GOCACHE=/private/tmp/fixora-pvc-gocache go test ./internal/storagefix -run 'Policy|Candidate' -count=1`; confirm expected failures.
- [x] Implement immutable authority and typed plans capturing PVC UID/resourceVersion, prior value, proposed value, StorageClass identity and resourceVersion when required.
- [x] Re-run the focused tests and confirm malformed policy, source-managed objects, unsupported statuses, and non-monotonic requests refuse.

### Task 2: Identity-bound verification and delivery lifecycle

**Files:** Create `verify.go`, `delivery.go`, `client.go`, `delivery_test.go`, `client_test.go`.

**Interfaces:** `Verify(ctx, Runtime, Plan) (*Receipt, error)` performs server-side dry-run of the exact authorized patch; `Apply(ctx, Runtime, Plan, *Receipt) (*Applied, error)` is single-use and compare-and-swap guarded; `Observe(ctx, Runtime, *Applied) (Outcome, error)` checks binding or expansion conditions.

- [x] Test valid dry-run and reject stale PVC/StorageClass UID or resourceVersion, changed policy digest, admission mutation, receipt replay, and unsupported API state.
- [x] Run focused tests and observe expected failures.
- [x] Implement typed client methods with JSON Patch `test` preconditions for UID/resourceVersion and exact authorized field; validate server response and never roll back an expansion.
- [x] Test binding completion and expansion observed capacity, FileSystemResizePending, missing consumer, replaced PVC, and inconclusive observation behavior.
- [x] Run focused tests, including race tests for policy/receipt use if supported.

### Task 3: CLI contract and output

**Files:** Modify `internal/cli/root.go`, `fix_prepare.go`, `help.go`; create `pvc_fix.go`, `pvc_fix_test.go`.

**Interfaces:** Add option `pvcPolicy string`, flag `--pvc-policy`, and `runPVCFixRuntime(...)` parallel to dedicated Service repair dispatch. Without policy, output the finding cause plus a concise exact policy prerequisite and state that no changes were made.

- [x] Test flag parsing, wrong target kind, `--preview` no writes, policy refusal, explicit apply confirmation, source ownership refusal, nonrollbackable warning before expansion, and machine-readable output.
- [x] Run focused CLI tests and confirm expected failures.
- [x] Implement minimal dispatch and text/JSON output; AI assessment may be rendered as explanatory text only.
- [x] Run `GOCACHE=/private/tmp/fixora-pvc-gocache go test ./internal/cli -run 'PVC' -count=1`.

### Task 4: Better deterministic storage evidence

**Files:** Modify `internal/analyzer/pvc.go`, `storage.go`, their tests, and evidence collection types as needed.

- [x] Test full identity chain and explicit partial/unavailable evidence for missing PV, denied StorageClass/PV/Pod reads, StatefulSet claim templates, multiple consumers, and topology constraints.
- [x] Run focused analyzer tests and confirm expected failures.
- [x] Add bounded evidence fields and concise root-cause summaries without changing existing status names or assigning unsafe recommendation patches.
- [x] Run focused analyzer tests and `go test ./test/coverage -count=1`.

### Task 5: Documentation and disposable verification

**Files:** Modify `docs/reference/remediation-coverage.md`, `docs/use-cases/storage-security.md`; create `hack/e2e-pvc.sh`, a disposable-cluster fixture, and `docs/guides/pvc-repairs.md` as needed.

- [x] Add coverage mapping tests for documented PVC finding outcomes.
- [x] Implement opt-in temporary-namespace tests for no-policy/preview immutability, exact class assignment, bounded expansion, and refused delete/reclaim/shrink actions; the script never targets `fixora-demo` or deletes a cluster.
- [x] Run the disposable-cluster test on the explicitly selected Terraform-managed `fixora-test` cluster; verify exact class assignment, CSI block expansion and node-side filesystem resize, bounded policy and cleanup. Destroy the temporary cluster after the test.
- [x] Run focused e2e shell syntax and opt-in guard checks, PVC-filtered repository-wide tests, race tests for `internal/storagefix`, `go vet ./...`, build, and `git diff --check`.
- [ ] Build the Jekyll documentation site; local Jekyll and Docker are unavailable.
- [x] Run the full Go suite and report any environment limitation; do not claim full-suite success unless it passes.

### Execution notes

- Focused storagefix, analyzer, CLI PVC, and coverage tests pass; repository-wide PVC-filtered tests pass; `go vet ./...` and `go test -race ./internal/storagefix` pass.
- Repository-wide `go test ./...` cannot pass in this sandbox because existing `httptest.NewServer` tests cannot bind localhost (`operation not permitted`). Go also reports that its module download cache under `/Users/baka/go/pkg/mod` is not writable. These failures are environment constraints, not PVC test failures.
- The disposable storage e2e ran against a temporary Terraform-managed EKS cluster. It verified no-policy behavior, preview immutability, exact policy-bound class assignment, CSI volume expansion, filesystem resize through a disposable mount pod, and refused shrink. Cleanup removed the test namespace/PVs/StorageClass; Terraform then destroyed all 62 managed resources.
- The documentation unit tests pass (18 tests), and the source checker reports no PVC-guide issues after adding required command effects. Whole-site source validation still reports missing front matter in `helm-source-delivery.md` and two pre-existing `docs/testing/` reports. Jekyll and Docker are unavailable locally, so the Actions site build could not be reproduced.

### Verification refresh — 2026-10-26

- `GOCACHE=/private/tmp/fixora-pvc-gocache go test ./... -count=1` passed.
- `GOCACHE=/private/tmp/fixora-pvc-gocache go test -race ./... -count=1` passed.
- `GOCACHE=/private/tmp/fixora-pvc-gocache go vet ./...` and `make build` passed.
- `python3 -m unittest tests.test_check_docs` passed (18 tests); `bash -n hack/e2e-pvc.sh` passed; the script's unset/negative confirmation guard exits before Kubernetes access.
- `python3 scripts/check_docs.py --source docs` reports missing front matter in `helm-source-delivery.md` and two untracked `docs/testing/` reports; none are PVC-guide issues. The site build could not run: this checkout has no Gemfile or Jekyll executable, and Docker is absent.
- The disposable PVC test had already passed against the temporary Terraform-managed EKS cluster and its namespace/PVs/StorageClass were cleaned up; Terraform destroyed the temporary cluster. This refresh did not contact AWS or modify any cluster.
- Independent review then required consumer/controller ownership resolution through the verified owner chain, consumer and StatefulSet-template rechecks during verify/apply/observe, and signal-safe E2E cleanup guarded by the per-run marker and captured namespace/StorageClass UIDs. Regression tests cover a controller-managed consumer without Pod-level markers and consumer/StatefulSet drift before delivery. The corrected E2E script passed `bash -n` and its no-confirmation guard; it was not rerun against AWS.
- Final independent review confirmed that failed namespace deletion, PV disappearance, or StorageClass deletion now makes the E2E script exit nonzero; the mock cleanup test reproduced that behavior. No further actionable findings remained.

## Plan self-review

- All design requirements map to Tasks 1–5. Expansion has no rollback contract; safety refusals are explicit in candidate, lifecycle, CLI, analyzer, and e2e coverage.
- Interfaces flow policy → plan → receipt → applied state → observed outcome without using AI output as authority.
- The five review-focus inputs are assigned tests in Tasks 1–4.
- This plan is intentionally staged; task signatures may be adjusted only when existing APIs prove a type mismatch, with tests updated first.
