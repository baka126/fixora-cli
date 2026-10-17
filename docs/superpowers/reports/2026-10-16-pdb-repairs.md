# Step 5b: PDB repair checkpoint

Branch: `codex/pdb-repairs`, based on `75a3971`.
Workspace: `/Users/baka/.codex/worktrees/pdb-repairs/fixora-cli`.
Status: implementation and acceptance scaffolding complete; final whole-branch review pending.

## Delivered behavior

- Precise deterministic PDB diagnosis, including canonical exact-target aliases, intentional disruption protection, empty selections, unhealthy replicas, stale/failed status, overlapping budgets and unreadable evidence. Display filters cannot hide repair inventories.
- Independent strict `PDBRepairPolicy` with exact workload/selector/budget and absolute approved availability bounds; AI may explain/propose but cannot grant authority.
- Dedicated stable Deployment/StatefulSet candidate gate, non-disruptive server dry-run, private single-use receipt, UID/resourceVersion-tested field patch, current-generation policy observation and explicit conflict-guarded inverse.
- Concise CLI, `--pdb-policy`, read-only preview/structured output, manual Helm values guidance and guarded reviewed raw/Kustomize source workflow.
- Dedicated local-kind acceptance fixtures/harness; stubbed harness test proves only uniquely named local cluster creation is attempted.

## Verification observed before review

Fresh `go test -count=1 ./...` passed after the alias regression fix. Full race suite passed, and `go vet ./...` passed. `make build` passed before the alias correction; the final binary is rebuilt at finish. Tagged E2E tests compiled; the local harness passed Bash syntax and its stubbed behavior test. Initial sandbox baseline failed because loopback listeners were denied; the elevated baseline and subsequent suites passed.

Docker is unavailable locally. Real local Kubernetes controller/eviction behavior was not exercised. No EKS or ONF writes, provider credit usage, image publication, real test PR, merge or deployment occurred. E2E compilation does not prove live controller acceptance.

## Scope and remaining work

Repairs require stable fully healthy unmanaged Deployment/StatefulSet replicas, complete evidence, approved floor/ceiling and no active HPA or overlapping PDB. Selector corrections require an empty current selection. PDB changes never evict/delete Pods, drain nodes, scale workloads or change unhealthyPodEvictionPolicy. Preflight/policy convergence is not a guarantee of actual drain success, future availability or application quorum.

Existing common workload, controller, Service, provider and source/Helm tests remain the regression boundary. HPA repairs are the next design slice; storage and node/admission/operator work remain separate later phases.

## Decisions

- Collect supplemental evidence failures for diagnosis, but reject all incomplete repair evidence. Cost: conservative repair refusals when RBAC is incomplete.
- Establish the default standalone operation deadline during candidate collection, earlier than the planned verifier task. Cost: a three-minute standalone collection limit.
- Acceptance checkpoint precedes the required whole-branch review; any review fixes and the final report follow. The reviewer remains a finish gate.

The approved spec and implementation plan are in `docs/superpowers/specs/2026-10-16-pdb-repairs-design.md` and `docs/superpowers/plans/2026-10-16-pdb-repairs.md`. The execution ledger remains in this plan's ignored `.superpowers/sdd/2026-10-16-pdb-repairs` workspace until final review is resolved.
