# Step 5a Service dependency routing checkpoint

Branch: `codex/service-dependency-repairs`, based on Step 4 `34c30c5`.
Implementation and the final review fix pass are complete. Final verification was refreshed on 2026-10-15; see results below.

## Delivered scope

Independent local `ServiceRepairPolicy` intent, exact Ready backend ownership/membership, selector replacement and selected targetPort correction for non-headless selector-managed ClusterIP/TCP Services. Supports existing same-namespace Deployment, StatefulSet and DaemonSet backends without demo-specific names, labels, commands or ports.

AI receives approved backend/selector/port data as proposal context, while the verifier image/commands remain outside this context. AI patches must exactly match independently approved intent. The existing provider contract and generic workload repair path remain unchanged. Without a Service policy, existing diagnosis/review continues and routing repair points to the local intent prerequisite.

Dedicated admission/temporary-Service verification checks every expected backend address and each Service IP with a fixed operator-approved digest-pinned probe image. Restricted verifier Pod and NetworkPolicy, exact EndpointSlice/Pod identity checks, fresh policy and Service readbacks, bounded reports and nonce-bound private receipts. UID-guarded cleanup confirms deletion; failure blocks delivery and exposes known leftovers.

Production delivery uses UID/resourceVersion JSON Patch tests and only edited routing fields. A fresh production session checks the real Service. Failure retains applied/uncertain state, never automatically retries or rolls back. Explicit inverse restores only edited fields, preserves absent/null and unrelated concurrent metadata/port changes, and refuses changed fields or replaced identities.

`--preview` and JSON/YAML output stay read-only even with apply/yes/source flags. Raw/Kustomize reviewed patch and exact-file Git/PR workflow retains source validation and partial-failure reporting. Raw patches still require manual merge into the manifest. Temporary network verification does not certify a rendered source change or reconciliation. Helm Service values translation stays manual with no automatic values write or false review-request claim.

## Verification

Executed locally: full fresh unit and race suites; vet; CLI/probe Go builds; E2E suite compilation with `e2e,e2e_delivery`; shell syntax and diff whitespace checks. Command details and final review results are recorded below.

Focused failures observed before fixes: undefined intent/candidate/probe/verifier/delivery APIs; inconsistent named targetPort across Pods; selected Service port not first/different backend name; encoded HTTP path preservation; stale temporary routing/public preview; missing/duplicate reports; admission rewriting working directory/session affinity; caller timeout silently capped; API TypeMeta differences; trailing multi-document AI proposals; HTTP fragments; deleting namespace/backend and manually owned endpoints; deletion accepted without actual cleanup; changed isolation policy; missing provider proposal context.

Not executed: Docker image build, local kind live routing/CNI/admission tests, EKS mutation, provider-credit calls, image publishing or a real PR/MR workflow. Docker is unavailable. The optional `hack/e2e-service-probe.sh` builds/pushes only to its dedicated loopback registry and runs disposable local fixtures; it has not been runtime validated here. Unit/fake results and tagged compilation are not live-cluster coverage.

Guide: `docs/guides/service-repairs.md`. Approved design/plan: `docs/superpowers/specs/2026-10-13-service-dependency-repairs-design.md` and `docs/superpowers/plans/2026-10-13-service-dependency-repairs.md`.

## Rulings

1. Overall verification timeout belongs to the parent session and matching Pod active deadline; removed the fixed binary's silent three-minute cap. Each check and target count remain bounded. Cost if wrong: standalone invocation can run up to 130 bounded checks rather than three minutes.
2. Delivery returns a private `*Applied` receipt and uses `production-contract-passed`. A nil receipt distinguishes pre-submit refusal from an uncertain submitted mutation; the status avoids claiming consumer recovery. Cost if wrong: new API consumers must explicitly handle partial/unknown outcomes and the contract-specific status.

## Final review and fixes — 2026-10-15

One fresh read-only whole-branch review (base `34c30c5`, implementation `abf6f14`) found seven Important issues, with no Critical or Minor findings. Each original failure was independently reproduced and fixed in one pass; no second review was dispatched.

1. Scalar targetPort output: numeric/named values now remain Kubernetes scalars in patches and previews; typed decoding and strategic selector replacement round trips pass.
2. Same-file source scope: PR preflight refuses an existing dirty working tree/index before temporary resources or source writes; existing allowed-file edits cannot be silently staged.
3. Rollback outcome: restored routing and all intended user metadata must match the field-only inverse before claiming rollback-restored. Admission rewrites remain uncertain outcomes.
4. Admission/freshness: owner references, finalizers and other user metadata are bound. Only explicit server-owned metadata is normalized; dry-run and production responses must preserve intended fields.
5. Log identity: the original verifier UID/spec/successful termination evidence is re-read after log retrieval. Name reuse and forged successful logs cannot issue a receipt.
6. Lost create responses: cleanup reconciles only a unique nonce/generated-template match, captures its real UID, then deletes with UID guards. Missing/list-denied/ambiguous/altered objects produce explicit incomplete cleanup and an inspection selector; unproven resources are not deleted.
7. Deadlines: default operation bounds cover the first API request; CLI preparation/apply receive bounded contexts, and explicit caller verification deadlines remain authoritative.

Observed RED→GREEN reproductions are saved in `internal/servicefix/review_regression_test.go` and `TestServiceCLIPRRefusesExistingSameFileChanges`. Additional tests cover numeric/named values, metadata freshness/production rewrites, exact recovery for Service/NetworkPolicy/Pod and recovery refusal cases.

Fresh final checks all exited 0:

- `GOCACHE=/tmp/fixora-go-cache go test -count=1 ./...`
- `GOCACHE=/tmp/fixora-go-cache go test -race -count=1 ./...`
- `GOCACHE=/tmp/fixora-go-cache go vet ./...`
- `GOCACHE=/tmp/fixora-go-cache make build`
- `GOCACHE=/tmp/fixora-go-cache go build -o /tmp/fixora-service-probe ./cmd/fixora-service-probe`
- `GOCACHE=/tmp/fixora-go-cache go test -c -tags='e2e,e2e_delivery' -o /tmp/fixora-service-repair-e2e.test ./test/e2e`
- `git diff --check` and `bash -n hack/e2e-service-probe.sh`

Deferred minors: none. Docker remains unavailable; live local/Kubernetes/image/CNI/real-provider/source-PR execution remains untested.

The review explicitly set aside automatic raw-manifest rewriting/a new render certificate because the approved spec retains manual reviewed source blocks. Ruling: keep that manual scope and truthful review claims; cost if wrong is that raw source still requires manual integration before reconciliation. This is not a certified source-render or application-recovery feature.

## Remaining roadmap

This implements Step 5a routing, not the entire cross-resource roadmap. Scaling/disruption, persistent storage, node/admission/operator repair families need separate intended-state policies and verifiers. External/selectorless/headless/mesh/operator Service routing, credentials, client authorization and full application recovery remain outside this automated contract.
