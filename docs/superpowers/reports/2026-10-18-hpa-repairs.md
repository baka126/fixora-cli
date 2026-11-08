# HPA request repair acceptance report

Date: 2026-10-18  
Branch: `codex/hpa-repairs`  
Implementation base: `02eea0a`

## Delivered

Fixora now diagnoses HPA generation, target, metric-condition and request-utilization failures. A strict local `HPARepairPolicy` can authorize exact missing CPU or memory requests for named regular containers on a same-namespace Deployment or StatefulSet. The dedicated runtime collects typed HPA/workload/Pod/owner/PodMetrics evidence, server-side dry-runs the narrow request patch, issues a private one-use receipt, applies with UID/resourceVersion tests, observes rollout and HPA metrics separately, and supports a guarded field-only inverse.

The CLI exposes `--hpa-policy`. Preview and JSON/YAML output remain read-only. AI receives bounded HPA and workload evidence but cannot authorize or change request values. Source-managed workloads remain review-only. The local kind harness is explicit opt-in and owns a unique cluster and kubeconfig.

## Final review hardening

An independent whole-branch review found gaps in the real Deployment owner chain, HPA alias dispatch, source ownership checks, HPA and target drift binding, request-presence observation, rollback container lookup and response validation, PodMetrics freshness/completeness, and admission metadata validation. These were fixed with regression tests. The runtime now resolves Pod→ReplicaSet→Deployment ownership, binds the HPA spec/UID/generation and target UID at preparation, rechecks source markers, requires fresh metrics for every regular container, verifies approved requests remain present during observation, and confirms rollback removed only those request fields.

## Verification

Passed:

- `go test ./test/coverage ./internal/hpafix ./internal/analyzer ./internal/cli ./test/e2e -count=1`
- `go test ./... -count=1`
- `go test -race ./... -count=1`
- `go vet ./...`
- `make build`
- `go test -c -tags='e2e,e2e_delivery' -o /tmp/fixora-hpa-e2e.test ./test/e2e`
- `bash -n hack/e2e-hpa.sh`
- `git diff --check`

The static harness tests confirm explicit opt-in precedes cluster creation and that cluster operations use the dedicated kubeconfig. Live kind execution was skipped because Docker is unavailable in this environment. The tagged controller test compiled but was not run against a live metrics-server or Kubernetes controller.

## Scope limits

Only autoscaling/v2 CPU/memory `Resource` metrics with `target.type=Utilization` can enter the candidate path. External/custom/container metrics, cross-namespace targets, unsupported controllers, multiple HPAs for one workload, incomplete ownership or metrics evidence, scaling transitions, stale status, and scaling limits remain blocked or review-only. An approved request is operator intent; it does not prove sustained capacity, future scale-out headroom, or application SLOs.

No EKS demo resources or external cluster were accessed during this work.

## Verification refresh — 2026-10-25

The implementation remains clean after the final review fixes. Fresh verification passed:

- `go test ./test/coverage ./internal/hpafix ./internal/analyzer ./internal/cli ./test/e2e -count=1`
- `go test ./... -count=1`
- `go test -race ./... -count=1`
- `go vet ./...`
- `make build`
- `go test -c -tags='e2e,e2e_delivery' -o /private/tmp/fixora-hpa-e2e.test ./test/e2e`
- `bash -n hack/e2e-hpa.sh`
- `python3 -m unittest tests.test_check_docs` (18 passed)

Loopback tests required sandbox escalation and passed. The local `kind` binary is available, but Docker is not, so live kind execution remains skipped; tagged tests compile, but do not establish live HPA-controller behavior.
The documentation source checker flags only the pre-existing missing front matter in `docs/helm-source-delivery.md`; HPA and PDB guide front matter has been added.

## Follow-up review — 2026-10-26

A fresh review found five additional edge cases in the pushed branch. Fixora now compares admitted CPU/memory quantities semantically across Kubernetes canonicalization, normalizes omitted empty resource maps when validating rollback, computes the rollback expectation before issuing the patch, refuses rollback after the target becomes source-managed, resolves ordinary Job/DaemonSet/CronJob owner chains while scanning namespace Pods, and selects the exact missing-request finding when an explicit HPA policy is supplied alongside generic metrics failures. Each case has a regression test. A second independent review found no actionable regressions.

After these fixes, focused HPA/CLI/analyzer/coverage tests and race tests passed; the final `go test ./... -count=1` rerun also passed. `go vet ./...`, `make build`, and `git diff --check` passed.
