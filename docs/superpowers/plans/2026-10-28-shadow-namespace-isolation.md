# Shadow Namespace Isolation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Execute every shadow attempt in a fresh namespace with proven default-deny ingress/egress, truthful inconclusive outcomes, and ownership-safe cleanup.

**Architecture:** Keep source workload inspection and patch validation unchanged, but redirect candidate execution into a generated Fixora namespace. Before candidate creation, establish that standard NetworkPolicy works using two TCP control pods, then apply deny-all and prove both directions are blocked. Cleanup deletes only the Fixora-owned namespace using UID preconditions and waits for deletion.

**Tech Stack:** Go, Kubernetes client-go typed API, Kubernetes NetworkPolicy, Go unit tests with fake clientsets, opt-in local kind/Calico E2E.

**Spec:** `docs/superpowers/specs/2026-10-28-shadow-namespace-isolation-design.md`

## Global Constraints

- The source namespace must never receive a shadow candidate Pod or policy.
- Default-deny ingress and egress must exist before candidate Pod creation.
- A candidate cannot be created unless both positive connectivity controls and both deny controls are conclusive.
- Active unsupported cluster-level networking policy, incomplete inventory, missing permissions, failed controls, or missing dependencies produce blocked/inconclusive verification with no delivery.
- Never copy Secrets, PVCs, credentials, or service-account tokens into the sandbox.
- Cleanup must prove namespace ownership, use UID preconditions, and wait for asynchronous deletion.
- Existing candidate identity, patch allowlist, and live apply guards remain intact.

## Review Focus

- Candidate code accidentally continues using the source namespace: test exact namespace creation order and candidate namespace.
- NetworkPolicy resources exist but CNI enforcement is absent: test each positive and negative control and ensure candidate creation is blocked on each failure.
- Namespaced dependencies or secret references turn an environment limitation into a candidate failure: test classification as inconclusive and no delivery.
- Namespace name is reused or ownership changes during cleanup: test ownership labels and UID preconditions.
- `--keep-shadow` bypasses isolation or falsely reports a delivery: test retained namespace metadata and delivery denial.

---

### Task 1: Namespace ownership primitives

**Files:** `internal/kube/typed.go`, `internal/kube/typed_test.go`

**Interfaces:** Add typed namespace create/get/delete methods. Delete takes namespace name and UID and sends a UID precondition; callers must pass the server-returned UID.

- [x] Test creation/get and UID-precondition delete request behavior with fake clientset reactors.
- [x] Run the focused tests and confirm they fail because the methods do not exist.
- [x] Implement `CreateNamespace`, `GetNamespace`, and `DeleteNamespaceUID` on `TypedClient`.
- [x] Run `go test ./internal/kube`.

### Task 2: Explicit isolation outcomes and per-attempt namespace

**Files:** `internal/shadow/types.go`, `internal/shadow/run.go`, `internal/shadow/clone.go`, `internal/shadow/run_test.go`, `internal/shadow/clone_test.go`

**Interfaces:** Add a stable verification status (`verified`, `failed`, `inconclusive`, `blocked`) and isolation summary to `Result`; keep legacy `Verified` but set it only for conclusive pass. Generate namespace names from run IDs, label namespaces with run ownership, and set clone/policy namespaces from the generated namespace. Reject unsupported `Egress` values; change default to deny.

- [x] Add tests asserting candidate/policy namespace routing, deny default, input validation, and delivery blocked on non-pass results.
- [x] Run those tests and confirm RED for missing namespace-routing behavior.
- [x] Implement the run namespace and outcome model with namespace creation preceding policy/pod creation.
- [x] Run `go test ./internal/shadow`.

### Task 3: NetworkPolicy enforcement controls

**Files:** `internal/shadow/isolation.go`, `internal/shadow/isolation_test.go`, `internal/serviceprobe/` only if the existing probe contract can be safely reused

**Interfaces:** Implement an isolation gate that reports conclusive pass/fail/inconclusive. Discover all nonstandard network-policy APIs and fail closed if any applicable policy objects exist or any listing is incomplete. Create disposable TCP witness pods in separate run-owned namespaces; prove connectivity both directions before policy, install sandbox deny-all, then prove both directions fail. Delete witness resources before candidate creation.

- [x] Add tests for passing and failing controls, unsupported policy APIs, and candidate blocking before a conclusive gate.
- [x] Run focused tests RED.
- [x] Implement the isolation gate using fixed, non-credentialed probe containers with bounded timeouts and no configurable arbitrary endpoints.
- [x] Run `go test ./internal/shadow ./internal/serviceprobe`.

### Task 4: Dependency-aware candidate classification

**Files:** `internal/shadow/clone.go`, `internal/shadow/run.go`, `internal/shadow/verify.go`, matching tests

- [x] Test missing ConfigMap, Secret, PVC, service-account token, image pull secret, and required volume cases. Sensitive and unsupported resources remain blocked; non-sensitive absent dependencies classify inconclusive.
- [x] Run tests RED.
- [x] Add a preflight dependency check and prevent environment-limitation outcomes from being reported as candidate regressions or delivered.
- [x] Run `go test ./internal/shadow`.

### Task 5: Owned namespace cleanup and operator docs

**Files:** `internal/shadow/run.go`, `internal/shadow/*_test.go`, CLI help text, `docs/guides/` shadow docs, `hack/` opt-in kind E2E harness

- [x] Test UID mismatch, missing ownership markers, asynchronous deletion wait, cleanup failure, and `--keep-shadow` behavior.
- [x] Run tests RED.
- [x] Implement ownership-checked namespace deletion, wait for `NotFound`, report retained namespace/policy, and update CLI/docs to state required permissions and deny-egress limitations.
- [x] Add an opt-in isolated kind/Calico acceptance run for both enforcement controls and a denied-egress candidate.
- [ ] Run the opt-in live E2E. E2E compilation and harness syntax pass, but this machine has no Docker executable/runtime.
