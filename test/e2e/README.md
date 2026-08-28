# Safety-gate e2e suite

This suite proves the four mutation safety gates hold against a real Kubernetes
API server: an ineligible plan cannot mutate a workload, a source-managed
workload cannot be applied directly, a failed shadow run cleans up after itself,
and a leaked secret never crosses the egress boundary. It guards gates, not
breadth — `hack/e2e-kind.sh` remains the exploratory scenario script.

## Running

    go test -tags e2e -timeout 20m ./test/e2e/...

Requires `kind` and `kubectl` on PATH **and a working container runtime (Docker,
Podman, or Colima) — `kind` cannot create a cluster without one**. A cluster
named `fixora-e2e` is created and destroyed per run.

| Variable | Effect |
|---|---|
| `FIXORA_E2E_KEEP=1` | Leave the cluster running after the run for fast local iteration |
| `FIXORA_E2E_BIN` | Use a prebuilt binary instead of compiling one |
| `FIXORA_E2E_KIND_CLUSTER` | Override the cluster name (default `fixora-e2e`) |

The `//go:build e2e` tag keeps this package out of `make test`, which stays
hermetic and needs no cluster.

## The gates

| Test file | Negative case (must be refused) | Positive case (must be allowed) |
|---|---|---|
| `gate_apply_test.go` | `fix` with no `--image`: the plan keeps its `TODO_` placeholder, `ApplyEligible` is false, the walkthrough declines and the live spec stays byte-identical | `fix` with `--container`/`--image`: the concretized plan is apply-eligible and the pinned image lands in the live spec |
| `gate_helm_test.go` | Each of six source-management markers (managed-by label, Helm release annotation, instance label, chart label, Argo CD tracking-id, Flux annotation) blocks direct cluster apply; the refusal points the operator at source delivery | A byte-identical deployment with no markers applies and is mutated |
| `gate_shadow_test.go` | A never-ready clone: `shadow.Run` takes the failure path yet still deletes the clone pod and the ingress-deny NetworkPolicy, and the production workload is unchanged | The production workload is never touched regardless of verification outcome (`TestShadowLeavesProductionUntouched`) |
| `gate_redact_test.go` | `why --ai --redact --include-logs`: the leaked secret appears in neither the request bodies the AI stub recorded nor the command output; `ValidateRevisedPatch` rejects hostNetwork, privileged, and multi-document patches | A benign resources-shaped patch passes `ValidateRevisedPatch` (`TestBenignAIPatchAccepted`) |

Every gate has a positive case on purpose. A gate that refuses *everything* is
completely broken yet would pass a refusal-only suite — the positive case is what
distinguishes a working gate from one that is stuck shut.

## Status: not yet executed

As of the initial commit, **no test in this suite has ever run.** It was authored
on a machine with no container runtime, so `kind` could not create a cluster and
no e2e test could execute. The CI `E2E` workflow
(`.github/workflows/e2e.yml`, which runs on push to `main` and `dev`) will be its
first execution.

Until that run is green, every assertion in this suite is unverified. Do not
treat the suite as evidence that the gates hold until CI has run it successfully
at least once.

## Required before trusting this suite

The suite is only meaningful if each test fails when the gate it guards is
broken. That fault-injection exercise has **not** been performed. Run it — on a
machine with a container runtime — before relying on this suite. Each command
below was checked against the current source; each mutation must be reverted
afterwards.

- [ ] **Apply gate.** In `internal/fix/planner.go`, force `p.ApplyEligible = true`
  (the assignment at the end of the plan-finalising function). Run
  `go test -tags e2e ./test/e2e/... -run TestApplyGateBlocksIneligiblePlan`.
  Expect **FAIL**: the deployment gets mutated and `requireUnchanged` reports it.

- [ ] **Helm gate.** In `internal/cli/root.go:1313`, make `sourceManaged` return
  `false` unconditionally. Run
  `go test -tags e2e ./test/e2e/... -run TestHelmGateRefusesDirectApply`.
  Expect **FAIL** on all six subtests.

- [ ] **Shadow cleanup.** In `internal/shadow/run.go:108`, make `cleanup` return
  immediately. Run
  `go test -tags e2e ./test/e2e/... -run TestShadowCleansUpOnFailure`.
  Expect **FAIL**: `requireGone` times out with the clone pod and NetworkPolicy
  still present.

- [ ] **Redaction.** In `internal/redact/redact.go`, make `Text` (line 60) and
  `KubernetesText` (line 69) each return their `value` argument unchanged. Run
  `go test -tags e2e ./test/e2e/... -run TestRedactionHoldsAtEgress`.
  Expect **FAIL**: the secret appears in a request body the AI stub recorded.

Every mutation must be reverted and `go build ./...` confirmed clean afterwards.
A test that cannot fail is worse than no test, because it reports safety that is
not there.
