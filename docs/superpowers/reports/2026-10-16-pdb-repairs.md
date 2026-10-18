# Step 5b: PDB repairs

Started 2026-10-16; final verification 2026-10-18.
Branch: `codex/pdb-repairs`, based on `75a3971`.
Reviewed checkpoint: `cc9e667`; verified review-fix commit: `ce7489cbed49110cc3a601c06944451dad7d0fe1`.
Workspace: `/Users/baka/.codex/worktrees/pdb-repairs/fixora-cli`.
Status: implementation and final review fixes complete. Preserve this branch/worktree for follow-up; no merge or release.

## Delivered behavior

- Precise deterministic PDB diagnosis, including canonical and policy-qualified exact-target aliases, intentional disruption protection, empty selections, unhealthy replicas, stale/failed status, overlapping budgets and unreadable evidence. Display filters cannot hide repair inventories.
- Independent strict `PDBRepairPolicy` with exact workload/selector/budget and absolute approved availability bounds. AI adds explanation/proposals but cannot approve availability intent or authorize writes.
- Dedicated stable Deployment/StatefulSet candidate gate, non-disruptive server dry-run, private shared single-use receipt, UID/resourceVersion-tested field patch, current-generation policy observation and explicit conflict-guarded inverse.
- Concise CLI and `--pdb-policy`; preview and structured output remain read-only.
- PDB raw/Kustomize/Helm source delivery is manual with no file, commit or PR writes. Existing generic writers cannot preserve exact PDB selector semantics; other workload/Service source paths are unchanged.
- Dedicated local-kind acceptance fixtures/harness; stubbed harness test proves only uniquely named local cluster creation is attempted.

## Final independent review and fixes

One reviewer read every changed file. Usage limits interrupted the same reviewer twice; its completed report identified six important findings, one minor and no declined-to-judge items. Each important finding was reproduced in a failing regression and corrected in one fix pass, followed by fresh full-suite/race checks. No second review was substituted for testing.

| Finding | Verified regression/fix |
| --- | --- |
| Generic source writers retain obsolete selector keys or remove the selector through a partial raw document | `TestReviewPDBSourceWritersCannotChangeSelectorIntent` RED→GREEN; manual zero-write fallback |
| Argo tracking labels permit direct mutation | `TestReviewArgoLabelCannotAuthorizeDirectRepair` RED→GREEN; existing tracking/Helm chart ambiguity blocks direct authority |
| Caller-mutated ChangedFields changes the private inverse | `TestReviewPublicPlanCannotChangeRollback` RED→GREEN; privately copied and bound plan fields |
| Receipt value copies permit another write attempt | `TestReviewReceiptValueCopyCannotRetryWrite` RED→GREEN; shared private consumption state |
| FIFO policy paths can block before file validation | `TestReviewFIFOPolicyDoesNotBlock` RED→GREEN; regular-file identity check and nonblocking/no-follow opens; approval recheck/replacement tests also pass |
| Qualified policy-group aliases bypass PDB handling | `TestPDBExactTargetUsesCanonicalDiagnosis` qualified cases RED→GREEN |

Deferred minor: the static advanced-help text lacks `--pdb-policy` and a PDB example. The flag is registered and documented in `docs/guides/pdb-repairs.md`.

## Verification observed

All commands below passed against the review fixes:

- `GOCACHE=/tmp/fixora-go-cache go test -count=1 ./...` — 22 tested packages.
- `GOCACHE=/tmp/fixora-go-cache go test -race -count=1 ./...` — 22 tested packages.
- `GOCACHE=/tmp/fixora-go-cache go vet ./...`.
- `GOCACHE=/tmp/fixora-go-cache make build`; binary rebuilt at the final committed checkpoint before handoff.
- `go test -c -tags='e2e,e2e_delivery' -o /tmp/fixora-pdb-e2e.test ./test/e2e` — compile only.
- `GOOS=windows GOARCH=amd64 go test -c -o /tmp/fixora-pdb-windows.test.exe ./internal/pdbfix` — compile only; Windows runtime was not exercised.
- `bash -n hack/e2e-pdb.sh`, harness behavior regression and `git diff --check`.

Initial sandbox baseline failed because loopback listeners were denied; the elevated baseline and subsequent suites passed. Tests needing local listeners/worktree writes used reviewed escalation.

Docker is unavailable locally. Real local Kubernetes admission/controller behavior was not exercised. No EKS or ONF writes, provider credit usage, image publication, real test PR, merge or deployment occurred. Tagged E2E compilation does not prove live controller acceptance. Run `hack/e2e-pdb.sh` in a Docker-enabled local environment before broad production rollout.

## Scope and remaining work

Repairs require stable fully healthy unmanaged Deployment/StatefulSet replicas, complete evidence, approved floor/ceiling and no active HPA or overlapping PDB. Selector corrections require an empty current selection. PDB changes never evict/delete Pods, drain nodes, scale workloads or change unhealthyPodEvictionPolicy. Preflight/policy convergence does not prove actual drain success, future availability or application quorum. There is no atomic transaction across all collected resources.

Existing common workload, controller, Service, provider and source/Helm tests remain the regression boundary. HPA repairs are the next design slice; storage and node/admission/operator work remain separate later phases. A dedicated exact-effect PDB source writer is future work.

## Decisions and costs

1. Collect supplemental evidence failures for diagnosis, but reject incomplete repair evidence. Cost: conservative refusals when RBAC/evidence is incomplete.
2. Establish the standalone operation deadline during candidate collection, earlier than the planned verifier task. Cost: three minutes is the default collection/operation cap unless the caller supplies a deadline.
3. Acceptance checkpoint precedes the required whole-branch review, following the execution workflow. Cost/risk: an acceptance commit alone is not a completed review gate; final findings were resolved before delivery.
4. Use the approved spec/Task 6 manual fallback for PDB source delivery. Cost: automated PDB source PR creation is deferred until an exact-effect writer exists; runtime repairs remain available.

Approved spec: `docs/superpowers/specs/2026-10-16-pdb-repairs-design.md`.
Executed plan: `docs/superpowers/plans/2026-10-16-pdb-repairs.md`.
The plan-specific ignored execution ledger is removed after verified review resolution; this report and Git history retain the durable record.
