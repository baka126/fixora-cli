# Verified Service Dependency Repairs Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. The user selected native execution earlier; preserve it. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add dynamic, policy-approved Service selector/targetPort repairs with dedicated endpoint/connectivity verification, guarded delivery and field-only rollback.

**Architecture:** A new `internal/servicefix` package owns Service intent, snapshots, proofs, verification and delivery; it does not widen workload repairpolicy or shadow authority. A fixed `internal/serviceprobe` binary checks only bound IP/port destinations using TCP or HTTP GET. A typed Kubernetes adapter and focused CLI route join these components to the existing provider, diagnosis and source-review flows.

**Tech Stack:** Existing Go 1.26.6, yaml.v3, Kubernetes v0.31.4 API/client-go, net/netip/net/http and crypto/rand. No new Go dependencies. A locally buildable probe image is an explicit runtime prerequisite; publishing it is outside this plan.

**Spec:** `docs/superpowers/specs/2026-10-13-service-dependency-repairs-design.md`, approved by the user's subsequent “yes”.

## Global Constraints

- Standard, selector-managed, non-headless ClusterIP Services and TCP ports only.
- Selector replacement and targetPort correction, individually or together, for an explicitly selected port; every other Service field must remain unchanged.
- Same-namespace backends owned by an explicitly identified Deployment, StatefulSet or DaemonSet.
- Existing backend workloads only: no dependency/database creation, Secret creation, endpoint authoring, label edits, workload recreation or persistent-data changes.
- Helm/GitOps direct delivery stays blocked. Service Helm values translation remains review-only.
- Preview/structured diagnostic output never creates resources. No provider-supplied authority, guessed destination, arbitrary shell command or automatic rollback.
- All Step 1–4 repairs remain protected by their existing tests. Preserve local Terraform/state/demo files. Native execution and one fresh whole-branch review at the end; no per-task implementer delegation.
- Start from `34c30c5` on `codex/service-dependency-repairs`. The spec checkpoint is `ee8d25b`; commit/push only product code/docs/tests. No merge or EKS deployment.

## Review Focus

1. A named targetPort resolves differently across Pods, or a numeric-looking string is treated as an integer: refuse ambiguous/invalid routing; preserve IntOrString semantics.
2. Pods/Service/EndpointSlices are recreated or modified between verification and apply: refuse stale proof; never redirect using a reused name alone.
3. A verifier timeout/cancellation or partial creation leaves resources behind: UID-guarded cleanup with a separate bounded context and truthful partial outcomes.
4. Existing selector keys or Service port ordering change: exact selector replacement and guarded selected-port identity; preserve unrelated ports/IPs and concurrent rollback changes.
5. Connectivity succeeds through one replica, a redirect/proxy or forged probe report: certify every expected address plus Service IP, with fixed contracts and bound probe identity; narrow claims.

## File map and shared interfaces

Create `internal/servicefix/policy.go`, `snapshot.go`, `candidate.go`, `proof.go`, `endpoints.go`, `verify.go`, `sandbox.go`, `client.go`, `delivery.go`, `rollback.go`, with matching focused tests. Create `internal/serviceprobe/probe.go`, `cmd/fixora-service-probe/main.go`, `build/service-probe.Dockerfile`. Create `internal/cli/service_fix.go` and tests; make minimal routing/flags/help changes in root.go/fix_prepare.go. Synthetic fixtures belong in `test/e2e/fixtures/service-repair/`; new E2E tests use existing tags/harness.

Canonical interfaces (types are defined only by the producing task):

```go
// Task 1, internal/servicefix: Authority has private copied bytes/digest/policy.
type Reader interface {
 GetNamespace(context.Context, string) (*corev1.Namespace, error)
 GetService(context.Context, string, string) (*corev1.Service, error)
 GetWorkload(context.Context, string, string, string) (map[string]any, error)
 ListPods(context.Context, string) ([]corev1.Pod, error)
 ListSlices(context.Context, string, string) ([]discoveryv1.EndpointSlice, error)
 ResolveOwner(context.Context, corev1.Pod) (kube.WorkloadOwner, error)
}
func LoadPolicy(path string) (*Authority, error)
func Collect(context.Context, Reader, *Authority) (Snapshot, error)
// Snapshot copied fields, all tagged json:"-" yaml:"-":
// Namespace *corev1.Namespace, Service *corev1.Service, Backend map[string]any,
// Pods []corev1.Pod, Slices []discoveryv1.EndpointSlice,
// Owners map[string]kube.WorkloadOwner (keyed by Pod UID).


// Task 2: Proof/candidate are immutable private authority.
type Plan struct {
 Status, Resource, Namespace, Diff, Scope string
 ChangedFields, Prerequisites, Warnings []string
 Proof *Proof `json:"-" yaml:"-"`
}
func Prepare(context.Context, Reader, *Authority, analyzer.Finding, string) (Plan, error)
func (Proof) CheckFresh(context.Context, Reader) error
func CheckEndpoints(*corev1.Service, []discoveryv1.EndpointSlice, Snapshot) ([]serviceprobe.Target, error)

// Task 3, internal/serviceprobe: typed wire contract, not a shell command.
type Target struct { Address string; Port int; PodUID string }
type Contract struct { Method, Path string; ExpectedStatus int; Timeout time.Duration }
type Request struct { Nonce string; Targets []Target; Contract Contract }
type Result struct { Nonce string; Checks []Check }
func Run(context.Context, Request) (Result, error)
// Check records exact address/port/PodUID and success/error/status, no body.

// Task 4: Runtime extends Reader with typed admission/resource operations.
func NewClient(*kube.TypedClient) (Runtime, error)
func Verify(context.Context, Runtime, Plan) (Verification, error)
func (Receipt) Check(*Proof) error
// Verification public Status/Scope/Summary strings, Warnings []string,
// Cleanup []CleanupResult; Receipt *Receipt tagged json:"-" yaml:"-".
// CleanupResult: ObjectRef plus Status/Detail strings; no raw resource spec.


// Task 5: Applied public Status/Resource/Namespace; immutable original,
// candidate, proof/receipt and changed-field inverse are private.
// Outcome public Status/Scope/Summary strings, Cleanup []CleanupResult,
// Warnings []string. Public flags never authorize mutation.

func Apply(context.Context, Runtime, Plan, *Receipt) (Applied, error)
func VerifyLive(context.Context, Runtime, Plan, Applied) (Outcome, error)
func Restore(context.Context, Runtime, Applied) (Outcome, error)

// Task 6, internal/cli:
func runServiceFix(context.Context, io.Writer, io.Writer, options, analyzer.Finding, fix.Plan) int
```

Public status constants: `no-change`, `prerequisite-required`, `candidate-prepared`, `source-review-required`, `verification-passed`, `verification-failed`, `cleanup-incomplete`, `service-applied`, `service-routing-verified`, `rollback-restored`, `rollback-refused`. Source-only mode is bound privately to proof; modifying public Scope cannot change it.

`Runtime` uses Task 1 Reader plus exact typed methods: `Admit(ctx,*Service)(*Service,error)`, `CreateService(ctx,*Service)`, `CreatePolicy(ctx,*NetworkPolicy)`, `CreatePod(ctx,*Pod)` returning their respective `*corev1.Service`, `*networkingv1.NetworkPolicy`, `*corev1.Pod` and errors; `ReadPod(ctx,ns,name)(*corev1.Pod,error)`, `PodLogs(ctx,ns,name,limit int64)([]byte,error)`, `DeleteOwned(ctx,ObjectRef) error`, and `PatchService(ctx,ns,name,patch []byte)(*Service,error)`. `ObjectRef` contains kind/namespace/name/UID. Do not use generic kubectl output as an executable mutation adapter or existing deletion methods without UID preconditions.

Decisions shared by tasks: policy bytes <=1MiB; <=64 selected backend Pods and <=128 backend addresses; reject overflow rather than sampling. Readiness requires explicit Pod Ready=True and non-terminating state. Snapshot all selector matches and require them to be Ready owned backends; a partially ready rollout needs investigation before this first automated route. Support IPv4/IPv6 literal addresses; verify all allocated Service IPs. Default per-check timeout 5s, allowed range 1–10s; overall verification defaults to 3m with the existing CLI timeout override, cleanup gets 15s independent of cancelled parent. Probe input/output <=64KiB; HTTP response bodies discarded with bounded reads, no proxy, redirect, DNS, credentials or custom headers. Verification reports operator-approved TCP reachability or exact HTTP status, never full application/consumer recovery or CNI enforcement from NetworkPolicy API acceptance alone.

### Task 1: strict intent policy and complete backend snapshots

**Files:** policy.go, snapshot.go and their tests. **Produces:** Authority, Reader, Snapshot, LoadPolicy, Collect. No mutations.

- [ ] Write `TestServicePolicyStrictIntent` with a `fixora.io/v1alpha1` / `ServiceRepairPolicy` document: metadata.namespace; spec.service.name; spec.backend.kind/name; spec.selector; spec.port.name/port/targetPort; spec.verification.method/path/expectedStatus/timeout/probeImage. Assert arbitrary names work; digest-pinned image and explicit selected port/selector are required; fields cannot come from AI. TCP method has no HTTP fields; HTTP allows a local `/` path (not `//`, query/fragment or absolute URL), status 200–399. Unknown fields, aliases/duplicates, multi-doc, oversized inputs, URL/symlink files and malformed IntOrString values refuse.
- [ ] Write `TestServiceSnapshotExactOwnedMembership` and `TestServiceSnapshotReadFailures`: exact Service/backend UID/namespace, all selected regular-container port mappings and Pod IPs; ReplicaSet→Deployment resolution uses `kube.ResolveWorkloadOwner`; unrelated/missing owner, recreated resource, non-Ready/terminating Pod, read denial, empty backend set and count overflow refuse. Never use name similarity or Pod phase as authority.
- [ ] Run `GOCACHE=/tmp/fixora-go-cache go test ./internal/servicefix -run 'ServicePolicy|ServiceSnapshot'`; expect RED, missing interfaces/behavior.
- [ ] Implement typed policy parsing through a validated yaml.Node then strict JSON decode; reject unsupported Services/protocols, selectorless/headless, publishNotReadyAddresses and externally/operator-managed endpoint sources. Copy raw field presence and original Service spec for later candidate/rollback checks. Separate existing current Service endpoints from proposed backend membership. No changes to existing workload baseline loaders. Namespace evidence/admitted verifier settings must remain bound. Required NetworkPolicy admission/readback failures block verification; underlying CNI packet enforcement is an operator deployment prerequisite, documented as unverified by this connectivity certificate rather than inferred from API acceptance.
- [ ] Run focused tests and `go test ./internal/servicefix`; expect PASS. Commit `feat(servicefix): bind approved Service intent and backend snapshots`.

### Task 2: exact Service candidate, proof and endpoint evidence

**Files:** candidate.go, proof.go, endpoints.go/tests. **Consumes:** Task 1 interfaces; references Task 3 Target type (define its shared data type now in serviceprobe/types.go, with Task 3 owning behavior). **Produces:** Plan/Proof, Prepare, CheckFresh, CheckEndpoints.

- [ ] Write `TestServiceCandidateExactFields` for selector-only, numeric/named targetPort and combined repairs. Assert obsolete selector keys are removed; IPs/families, public ports/protocols, labels/annotations and other port entries remain unchanged. Check no-op returns no mutation proof. An AI proposal is optional; if present it must equal the operator-approved changes, not broaden them, and pass the existing 90–100 confidence range. A policy-derived candidate with no AI patch has independent operator authority; it must not be labelled AI-verified.
- [ ] Write `TestServiceProofNoReplayOrPolicyBypass`: confidence/identity/recovery/source blockers are retained; saved JSON contains no authority; modified policy file, Service/backend UID/configuration, membership/port/address or verification contract refuses freshness. Allow source review only under the existing source-ownership rules, without creating direct-apply authority. Reject arbitrary port additions/deletions, malformed Service envelope and unsupported kinds.
- [ ] Write `TestServiceEndpointIdentityAndReadiness`: actual controller-owned Service UID, exact Pod targetRef namespace/name/UID/address, slice manager/controller ownership and matching address family/protocol/port required; aggregate shards and deduplicate repeated addresses. Missing Ready=True, terminating, manual/extra/missing endpoints, named-port disagreement and number/string confusion refuse. Empty/error reads are not healthy.
- [ ] Run `go test ./internal/servicefix -run 'ServiceCandidate|ServiceProof|ServiceEndpoint'`; expect RED. Implement canonical typed comparisons and immutable private binding; retain raw presence for inverse/source patches. Use Kubernetes Service schema for strategic merge of a supplied AI proposal, then compare the whole candidate to the approved field change. Do not change shadow.ValidateCandidatePatch or workload ApplyEligible semantics.
- [ ] Run `go test ./internal/servicefix ./internal/fix ./internal/shadow ./internal/repairpolicy`; expect PASS. Commit `feat(servicefix): certify exact routing changes and endpoint identity`.

### Task 3: fixed IP-only connectivity verifier

**Files:** serviceprobe/probe.go/types.go/tests, cmd/fixora-service-probe/main.go/main_test.go, build/service-probe.Dockerfile. **Consumes:** Target/Contract/Request wire types; produces Run/Result/Check and a local-build image recipe.

- [ ] Write `TestServiceProbeChecksEveryTarget` using local listeners: one failing replica prevents success, explicit TCP close versus exact HTTP status claims, IPv4/IPv6 formatting and bound nonce/Pod UID results. `TestServiceProbeRejectsRedirectProxyAndUnsafeInput` asserts disabled redirects/environment proxies, literal-IP destinations only, strict JSON/trailing-data/output limits, no credentials/custom commands and cancellation/timeouts. No response body/log secret appears in results.
- [ ] Run `go test ./internal/serviceprobe ./cmd/fixora-service-probe`; expect RED. Implement direct net.Dialer and fixed http.Transport (Proxy=nil, redirect refusal, total timeout) with deterministic bounded results. Reject invalid ports/addresses/duplicate targets before any connection; no successful partial response on error.
- [ ] Binary accepts exactly one bounded JSON request from --request-json (the fixed Pod argument) or stdin when that argument is absent, and writes one JSON result; nonzero exit on any failed contract. Add a multi-stage scratch/non-root image recipe using the repository Go toolchain; entrypoint is the fixed binary. Runtime digest comes only from the approved policy, never the provider. No image push or new registry setup.
- [ ] Run package tests and `go build -o /tmp/fixora-service-probe ./cmd/fixora-service-probe`; expect PASS. Test image locally only when Docker is available, reporting absence explicitly. Commit `feat(serviceprobe): add fixed bounded backend connectivity checks`.

### Task 4: dedicated sandbox verification and typed lifecycle

**Files:** servicefix/client.go, sandbox.go, verify.go/tests. **Consumes:** Tasks 1–3; **produces:** Runtime/ObjectRef, Verification/Receipt, NewClient, Verify. Add typed methods locally around TypedClient.Clientset; do not loosen existing kube fallback/delete methods.

- [ ] Write `TestServiceVerifyAdmittedExactCandidate` and `TestServiceVerifyEveryEndpointAndServiceIP`: dry-run preserves candidate; temporary Service is in backend namespace with GenerateName and only copied needed routing fields; real slice ownership/ports precede bounded verifier request; every backend address and every Service IP must pass. Incorrect/admission-rewritten candidate, forged/duplicate/missing result entry, wrong nonce/probe UID/image and a zero exit without matching results yield no receipt.
- [ ] Write `TestServiceSandboxIsolationAndCleanup`: create policy before probe Pod, disable token automount, non-root/read-only rootfs/drop capabilities/seccomp, no Secrets/PVC/host access, restrictive ingress/egress and fixed `/fixora-service-probe --request-json <bound-json>` argv with no shell; admitted Pod settings/image must agree. API/isolation/admission errors refuse. A cancelled or partial create cleans only recorded UIDs using a fresh <=15s cleanup context; deletion conflict leaves a reported cleanup warning, never deletes a replacement.
- [ ] Run `go test ./internal/servicefix -run 'ServiceVerify|ServiceSandbox'`; expect RED. Implement Runtime and coordinated lifecycle; callbacks return created identities even on later failure. Keep all created resources in a private session ledger. Trust the fixed operator-approved verifier, not arbitrary provider commands or unspecified NetworkPolicy packet enforcement. Lack of required APIs/isolation configuration stays a prerequisite.
- [ ] Use server dry-run on original UID/RV preconditions; compare whole supplied candidate fields after known API defaulting, not just selector. Bound polling to parent verification deadline; readbacks/witness must match captured membership before receipt. Cleanup on success/failure/cancellation; cleanup failure makes verification non-deliverable and reports owned leftovers.
- [ ] Run `go test ./internal/servicefix ./internal/serviceprobe` and `go test -race ./internal/servicefix ./internal/serviceprobe` with client-go fake reactors and local transport seams; expect PASS. No fake-client result is described as real CNI/network validation. Commit `feat(servicefix): verify routing through owned temporary resources`.

### Task 5: guarded production apply, live gate and field inverse

**Files:** delivery.go, rollback.go/tests. **Consumes:** Runtime, Plan/Proof, Receipt; **produces:** Applied/Outcome, Apply, VerifyLive, Restore.

- [ ] Write `TestServiceApplyRequiresFreshReceipt`: dry-run/probe receipt from the identical private proof is mandatory; changed Service RV/UID/spec or backend membership before apply refuses. Actual JSON Patch tests UID and resourceVersion and updates only full selector plus selected targetPort. Guard exact port identity/order before using its array index; other ports/Service IPs remain unchanged.
- [ ] Write `TestServiceLiveGateAndPartialOutcomes`: production slice convergence and every approved connectivity check required; connectivity success never implies consumer recovery. Failed post-apply checks/cancel/read errors retain an Applied receipt and truthful changed-state report. Production verification creates only a fresh bounded verifier session, not another target Service, and cleans its owned objects.
- [ ] Write `TestServiceRollbackFieldConflicts`: inverse restores only edited fields, distinguishes absent/null, tests UID/current candidate edited values and preserves unrelated concurrent metadata/ports. Replaced Service or changed edited field refuses; unchanged edited fields can restore while preserving unrelated changes. Restoring a broken original configuration reports rollback-restored, not healthy.
- [ ] Run `go test ./internal/servicefix -run 'ServiceApply|ServiceLive|ServiceRollback'`; expect RED. Implement typed JSON Patch and private Applied inverse, context-bounded live verification and explicit caller-confirmed Restore. No server-side apply takeover, automatic retry against new resourceVersion or automatic rollback.
- [ ] Run `go test -race ./internal/servicefix ./internal/serviceprobe` plus `go test ./internal/ops ./internal/coordinate`; expect PASS. Commit `feat(servicefix): guard Service delivery and field-only rollback`.

### Task 6: CLI, reviewed source output, synthetic fixtures and checkpoint

**Files:** cli/service_fix.go/tests; minimal root.go/fix_prepare.go flags/routing/help; docs/guides/delivery.md and reference/remediation-coverage.md; test/coverage/remediation.json only for real changed capability profiles; docs/guides/service-repairs.md; test/e2e/gate_service_test.go and fixtures/service-repair.

**Consumes:** all preceding task interfaces. **Produces:** runServiceFix and --service-policy <local-file>; no other policy flags, provider response schema or global patch-family changes.

- [ ] Write `TestServiceCLIApprovedPolicyDynamicAIAndNoAI`: existing analysis/provider enrichment remains shared; policy-driven deterministic and same-policy AI candidates use the new Service route only when the exact target is a Service. Without intent/probe image show the explicit prerequisite; mixed/unsupported AI patches remain review-only. Wrong policy/resource/ns and genuine blockers refuse. Workload flags/loaders/working patches are unaffected.
- [ ] Write `TestServiceCLIPreviewAndStructuredNoMutation`: --preview/JSON/YAML, even with --yes/--apply, never create probe/Service/policy, write source, commit, push or apply. Concrete routing diff and scoped validation are reviewable before existing confirmation. --quick/--force-risky/--keep-shadow cannot forge or retain Service authority/resources.
- [ ] Write `TestServiceCLIDeliveryAndRollbackConsent`: explicit --delivery cluster or existing --apply requires confirmation; source-managed direct delivery stays blocked. --shadow-verify/--delivery patch can verify and save a reviewed patch without touching production. Failed gate prompts for rollback using existing input abstractions; --yes does not silently authorize rollback. Partial failures disclose written/applied/cleanup state.
- [ ] Write `TestServiceCLISourceClaimsAndScope`: raw/Kustomize use existing reviewed source/PR validation and exact file scope; certificate does not relax existing guards or fabricate reconciliation. Source title/body clearly distinguishes temporary routing verification from production health. Helm returns manual source review with no values write or false PR-generated claim. Refuse no-op and stale source, preserve unrelated changes/remote failure state, redact private evidence.
- [ ] Run `go test ./internal/cli ./internal/servicefix -run Service`; expect RED. Implement focused routing after shared finding preparation, with a Service-specific AI patch classifier; keep generic workload selectAIRepair behavior and provider contracts unchanged. Default output is identity/cause/selector-port diff/verification scope/next action; proof carries membership detail.
- [ ] Add synthetic Deployment/StatefulSet/DaemonSet-backed fixtures with arbitrary names, wrong selectors/targetPorts, two backends and readiness/mixed-owner refusal variants. Local tagged E2E creates an isolated namespace and locally built probe image, verifies numeric/named routing and rollback, and deletes owned test resources. No Docker => explicitly not executed. No EKS reset, credentials or provider credits needed for unit tests.
- [ ] Run fresh `GOCACHE=/tmp/fixora-go-cache go test -count=1 ./...`, `go test -race -count=1 ./...`, `go vet ./...`, `make build`, `go build -o /tmp/fixora-service-probe ./cmd/fixora-service-probe`, `go test -c -tags='e2e,e2e_delivery' -o /tmp/fixora-service-repair-e2e.test ./test/e2e`, and `git diff --check`; expect exit 0. Runtime/live coverage is reported separately from compilation/fakes.
- [ ] Perform one fresh whole-branch review against `34c30c5`. Reproduce Important/Critical findings RED→GREEN and finish with green checks. Record scope, image prerequisite, executed/untested surfaces and decisions in a durable report. Commit product-only changes and push codex/service-dependency-repairs; no merge, image publish, real PR or EKS deployment.

## Plan self-review and handoff

Spec intent/scope map to Task 1; candidate and endpoint identity to Task 2; fixed trusted probe to Task 3; dedicated verification/cleanup to Task 4; apply/live gate/rollback to Task 5; AI/non-AI UX, reviewed source claims and regression to Task 6. Every Review Focus condition has a named test. Shared signatures are defined above once; Runtime returns real typed objects/UIDs and only Receipt/Applied carry private authority. No result flag is an executable certificate.

Service transport verification deliberately does not establish client NetworkPolicy permissions, dependency authentication, all application semantics or CNI enforcement. The bounded trusted verifier and existing isolation patterns restrict the test; deployment prerequisites and runtime admission errors remain visible. Unsupported Service variants and later scaling/storage/node/operator repair families remain review work.

Self-review resolved the Pod transport interface: the fixed binary supports a bounded --request-json argument, so Kubernetes verification needs no attach/exec or stdin injection. Shared data-only Target types precede the endpoint checker; transport behavior follows in Task 3. No undefined cross-task mutation adapter remains.

Implementation starts after the user reviews this written plan. Preserve the selected native execution method; do not ask the user to choose it again.
