# Step 3 Verification Policies Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking. The user has already selected native/inline execution; preserve that method.

**Goal:** Add proof-bound startup, placement, security and coordinated repairs without regressing existing Fixora fixes.

**Architecture:** A small `internal/repairpolicy` package owns opaque authority/proof/verification receipts and their live bindings. Family validators use existing workload envelopes, shadow execution and guarded delivery. Four sequential family plans yield independently testable checkpoints; AI transports/contracts and existing repair families remain intact.

**Tech Stack:** Go, existing kube Reader/TypedClient, Kubernetes API/client-go, existing YAML and resource.Quantity dependencies. No new dependencies.

**Spec:** `docs/superpowers/specs/2026-10-05-verification-policies-design.md` (approved by the user's “continue”).

## Global Constraints

- “No scenario names, images, commands, UID values, ports or node labels are built into repair decisions.”
- “The model's confidence and narrative do not establish operator intent.”
- “Required verification cannot be bypassed with quick, yes or force flags.”
- Preserve existing image/resources/env/probe/filesystem behavior, Deployment demo regressions and provider response contracts.
- Keep step 2 UID/resourceVersion guarded apply, immutable-target/future-run outcomes, source ownership and Secret/PVC/host/service-account restrictions.
- Product implementation begins only after review of this plan. Inline execution is already selected.
- Keep local Terraform/state/demo files out of commits. No live EKS/node/policy mutations are needed for unit verification.

## Review Focus

1. Serialized/replayed plans and model-supplied verification flags must never authorize delivery: shared-core proof round-trip tests.
2. Placement sources with pod affinity/anti-affinity, topology spread, custom scheduler or nodeName can produce misleading sanitized shadows: placement refusal tests.
3. Older healthy Pods plus a new admission-denied revision must not be treated as either universally healthy or historically broken: security revision-link tests.
4. A sidecar/init failure or multiple failing containers must not authorize replacing another container's command: startup selection tests.
5. Step-two failure and concurrent edits during rollback must preserve unrelated changes and report only mutations actually made: coordinated fault-injection tests.

## Repository map

- Create `internal/repairpolicy/{proof,baseline}.go`: binding and authority only; imports kube/standard libraries, never fix/cli/shadow/analyzer.
- Add opaque `PolicyAuthority *repairpolicy.Authority` and `PolicyProof *repairpolicy.Proof` to fix.Plan and `PolicyReceipt *repairpolicy.Receipt` to shadow.Result, all `json:"-"`. Add only sanitized public summaries to structured output.
- Create `internal/cli/fix_prepare.go`: share the existing fix preparation pipeline with coordinated execution; leave option parsing and command routing in root.go.
- Family files, interfaces and tests are specified in the four linked plans below.
- The CLI also stores a `PolicySelection string` on Plan for an explicitly selected family; this selector grants no authority without the runtime-only proof/authority.
- `internal/shadow/run.go` remains the execution/cleanup engine; `internal/ops/controller.go` remains the guarded live preflight/readback engine.

## Shared interfaces

All family plans consume these exact definitions/signatures. No proof is accepted from JSON/AI. Policy-aware shadow execution must require the correct proof before creating any resources; a caller-supplied strategy/result flag is insufficient. Opaque types keep their trusted state in unexported fields.

```go
// internal/repairpolicy/proof.go
// Target includes Kind, Name, Namespace, UID, PodName, PodUID strings.
type Target struct { Kind, Name, Namespace, UID, PodName, PodUID string }
type Snapshot struct {
    Target Target
    Object map[string]any
    Pod *kube.Pod
    Namespace map[string]any
    Nodes []kube.Node
    Events []kube.Event
}
type Authority struct { /* private normalized object/digest/provenance */ }
type Proof struct { /* private family/identity/template/candidate/authority bindings */ }
type Receipt struct { /* private matching proof and observed witness */ }
type Summary struct { Family, Result, Detail string; ChangedFields, Caveats []string }
func Collect(ctx context.Context, r kube.Reader, target Target) (Snapshot, error)
func ReadBaseline(path string) (*Authority, error)
type ValidatePatchFunc func(patch, strategy, resource, namespace string) error
func BindCommon(snapshot Snapshot, patch, strategy string, validate ValidatePatchFunc) (*Proof, error)
func VerifyCommon(ctx context.Context, r kube.Reader, proof *Proof, patched *kube.Pod, patch string) (*Receipt, error)
func (p *Proof) Check(snapshot Snapshot, patch string) error
func (p *Proof) Summary() Summary
func (r *Receipt) Check(proof *Proof, patch string) error
func (r *Receipt) Summary() Summary
```

Collect reads the target and optional failing Pod, verifies requested UIDs and the controlling-owner relationship, and captures immutable copies. Namespace/nodes/events are populated by each family only when needed. Bindings use SHA-256 over deterministic normalized JSON; candidate document/container/array semantics remain exact. A private binding helper is used only after a family validator establishes authority and causal proof. Binding alone is not a successful verification receipt. BindCommon accepts only the existing image/fix-architecture/resources/env/probe/filesystem families and invokes the supplied existing candidate validator. VerifyCommon is called by shadow.Run only after its existing readiness/soak check succeeds; batch outcomes remain excluded from coordinated execution. For all families, receipt methods re-read the observed shadow Pod through the reader, verify that observation agrees with the supplied Pod, and bind its UID, namespace, session labels and exact patch. They never accept a result flag from an AI/JSON response.

ReadBaseline enforces one YAML document, a maximum of 1MiB, valid workload identity/envelope, and no Secret references; no raw baseline is put in provider prompts or proof summaries. Explicit fields grant authority; omitted fields do not. A whole nodeSelector field grants authority to its exact approved nonempty map; security authority applies to explicitly present permitted leaves.

## Task 1: shared binding and immutable authority

**Files:** Create repairpolicy/proof.go, baseline.go and corresponding tests; modify fix/planner.go and shadow/types.go.
**Produces:** the interfaces above; opaque fields on Plan/Result; sanitized summaries.

- [ ] Write `TestProofRejectsChangedIdentityTemplateCandidateAndAuthority`, `TestProofAndReceiptCannotRoundTripThroughJSON`, `TestBaselineSingleDocumentSizeAndSecretRefusals` and `TestAuthorityDoesNotAuthorizeOmittedFields`. Assert UID/Pod UID/template/candidate changes refuse; unmarshaled Plan/Result has no trusted proof/receipt; 1MiB+1/multi-document/Secret inputs refuse; summaries omit raw baseline/env/commands.
- [ ] Run `GOCACHE=/tmp/fixora-go-cache go test ./internal/repairpolicy ./internal/fix ./internal/shadow`; verify new-test failure before adding implementation.
- [ ] Implement the interfaces and tests' binding rules using existing kube helpers. Deep-copy mutable maps/slices when capturing snapshots. Keep family admission and receipt creation private until the family tasks supply their verifiers.
- [ ] Run the same packages and existing demo/controller tests; require PASS without changing expected repair outcomes.
- [ ] Commit only these code/test files: `feat: bind repair policy proofs to live targets`.

## Family execution order

- [ ] Execute [startup plan](2026-10-05-verification-policies-startup.md), tasks S1–S2.
- [ ] Execute [placement plan](2026-10-05-verification-policies-placement.md), tasks P1–P2.
- [ ] Execute [security plan](2026-10-05-verification-policies-security.md), tasks H1–H2.
- [ ] Execute [coordinated plan](2026-10-05-verification-policies-coordinate.md), tasks C1–C2.

Each family extends the package's private proof builder with its own causal/authority checks. Existing generic shadow validators keep rejecting the new fields unless the matching bound proof is supplied on the policy-aware execution path. Edited/retried patches must invalidate and rebuild proof; trusted startup retries remain disabled. Any restored saved plan must collect fresh evidence, not deserialize a receipt.

## Task 2: final coverage, review and checkpoint

**Files:** docs/reference/remediation-coverage.md, controller-repairs.md, commands.md, configuration-and-output.md; test/coverage/remediation.json and inventory tests when statuses/profiles are added; relevant E2E fixtures/tests only.

- [ ] Update actual profile/target outcomes after each implemented family; preserve explicit review-only and future-run states. Add CLI help/output documentation for repair-baseline/repair-policy and concise proof summaries.
- [ ] Run `GOCACHE=/tmp/fixora-go-cache go test -count=1 ./...` and `go test -race -count=1 ./...`, escalating for local httptest listeners when required; require all PASS.
- [ ] Run `GOCACHE=/tmp/fixora-go-cache go vet ./...`, `GOCACHE=/tmp/fixora-go-cache make build`, and `go test -c -tags='e2e,e2e_delivery' -o /tmp/fixora-policy-e2e.test ./test/e2e`; require exit 0. Compilation is not cluster execution.
- [ ] Run a fresh whole-branch review against `a9bc256`; resolve critical/important findings with regression tests, then verify changed packages again.
- [ ] Check staged paths for Fixora-only scope, commit final coverage/docs, and report tested versus untested live surfaces. Keep the completed step 2 branch intact. Push the step 3 implementation branch when complete under the user's existing checkpoint/push workflow.

## Self-review

The four spec slices map to S1–S2/P1–P2/H1–H2/C1–C2; shared replay/identity safety maps to task 1; help/inventory/regression and final review map to task 2. Root interfaces are referenced consistently by child plans. Dedicated verification and field-scoped rollback are included; unsupported storage/fleet/dependency environments remain review, not claimed coverage. There are no additional provider integrations or universal repair promises.
