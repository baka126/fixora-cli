# Validated Helm Source Delivery Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. The user selected native/inline execution earlier; preserve that method. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Translate supported workload repairs into explicit values-file edits certified by exact chart-wide render effects, with guarded local/PR delivery and truthful validation outcomes.

**Architecture:** Add a separate typed Helm source pipeline beside the existing read-only suggestion path. Keep rendering, values candidate generation, effect certification and source writes in focused repo files; the CLI supplies existing live-policy/shadow checks and delivery confirmation. Runtime-only proof binds all source inputs and edited bytes; neither heuristic mapping nor AI confidence grants delivery authority.

**Tech Stack:** Go 1.26.6, existing yaml.v3, Kubernetes strategicpatch/resource helpers, existing Helm/kubectl CLIs and Git/PR workflow. No new Go dependencies.

**Spec:** `docs/superpowers/specs/2026-10-10-helm-source-delivery-design.md`, approved by the user's 2026-10-12 “continue”.

## Global Constraints

- “Preserve the working common repairs, provider contracts and step 3 authority/verification gates.”
- “Values and source paths never come from an AI response.”
- “A source PR is not reported as a healthy production rollout.”
- “No write is allowed during preview or on failed validation.”
- Preserve `fix.Plan.ApplyEligible` as the automatic mutation gate; review-only source writes retain existing explicit source-review consent and never claim runtime verification.
- No direct Helm/GitOps apply, Helm upgrade, dependency downloads, post-renderer execution, DNS enablement, schema bypass, automatic release-values extraction or EKS mutation.
- Automated mapping initially covers image/resources/ConfigMap env/probe fields. Startup/placement/security remain their step 3 policies, without adding source authorization in this checkpoint. Arbitrary template rewrites stay review-only.
- Legacy raw/Kustomize paths and missing-context Helm diagnostics stay intact. No scenario names, images, ports, sizing values or chart-specific key dictionary in decisions.
- Keep local infrastructure/state/demo/testing files out of commits. Use the existing dedicated branch from `cb9bd3e` and preserve unrelated local edits.

## Review Focus

1. A chart's source labels, generated names, nested namespaces or duplicate documents could select the wrong target: exact inventory and ambiguity tests in Task 1.
2. A values key can feed an image helper, sidecar, Service, hook or aliased subchart: whole-inventory side-effect refusal and type tests in Tasks 2–3.
3. Helm output/error text and Git diffs can expose unrelated Secret/values content: private raw structures and edited-key-only preview/error tests in Tasks 1, 3 and 5.
4. Source files, the Helm executable or live target can change between certification/confirmation/write/commit: input freshness and cancellation tests in Tasks 3–5.
5. A source ownership blocker can accidentally bypass a genuine repair-policy blocker or make a source PR sound healthy: review/runtime scope and direct-delivery refusal tests in Task 5.

## Repository map and shared interfaces

Create `internal/repo/helm_context.go` (confined input snapshot/render execution), `helm_inventory.go` (exact YAML inventory/semantic equality), `helm_candidates.go` (typed values edits), `helm_effect.go` (certification), and `helm_write.go` (fresh guarded source write), with matching `_test.go` files. Add `internal/cli/helm_source.go` and `_test.go` for flag/context assembly and delivery integration; keep command routing in root.go. Reuse existing read-only Helm helpers only as suggestion inputs, not proofs.

The exact shared types/signatures below are produced by the indicated tasks. Trusted byte buffers, render documents, proofs and receipts are omitted from JSON/YAML. Public summaries contain only sanitized identifiers, statuses and permitted edited-field previews.

```go
// Task 1: internal/repo/helm_context.go, helm_inventory.go
// Paths are local repository-relative inputs; capture resolves/confines them.
type HelmRenderContext struct {
    RepoPath, ChartPath, Release, Namespace, EditValuesPath string
    ValuesFiles []string // command-line order, never WalkDir order
    RenderMode, KubeVersion string // RenderMode: install or upgrade
    APIVersions []string
}
type HelmInputs struct { /* private immutable file bytes/hashes + renderer identity */ }
type ResourceKey struct { APIVersion, Kind, Namespace, Name string }
type HelmInventory struct { /* private document maps, canonical order-independent digest */ }
type HelmRenderer interface {
    Render(context.Context, HelmRenderContext, *HelmInputs, []byte) ([]byte, error)
    Lint(context.Context, HelmRenderContext, *HelmInputs, []byte) error
}
func CaptureHelmInputs(HelmRenderContext) (*HelmInputs, error)
type HelmScopeFunc func(ResourceKey) (bool, error) // true = namespaced
func ParseHelmInventory([]byte, string, HelmScopeFunc) (HelmInventory, error)
func (HelmInventory) Digest() string
func (HelmInventory) Target(ResourceKey) (map[string]any, error) // private copies; not output
func (HelmInputs) CheckFresh() error
func NewHelmRenderer() HelmRenderer

// Task 2: internal/repo/helm_candidates.go
// Values paths are JSON pointers, supporting keys with dots/slashes unambiguously.
type HelmValuesEdit struct { Pointer string; Value any `json:"-"` }
type HelmValuesCandidate struct { /* private edits + modified bytes */ }
type HelmMapping struct { FieldPointer, ValuesPointer string }
func ProposeHelmValues(*HelmInputs, HelmInventory, ResourceKey, string, []HelmMapping) ([]HelmValuesCandidate, error)

// Task 3: internal/repo/helm_effect.go
// Caller maps skipped/unavailable validation to explicit pending statuses.
type HelmValidation struct {
    Status, Summary string
    Admitted map[string]any `json:"-" yaml:"-"`
}
type HelmSourceDeps interface {
    Renderer() HelmRenderer
    ReadTarget(context.Context, ResourceKey) (map[string]any, error)
    Scope(context.Context, ResourceKey) (bool, error)
    ServerValidate(context.Context, string) (HelmValidation, error)
    ShadowVerify(context.Context, fix.Plan, string) (*repairpolicy.Receipt, error)
}
type HelmSourceRequest struct {
    Context HelmRenderContext
    Finding analyzer.Finding
    Plan fix.Plan
    Mappings []HelmMapping
    VerifyRuntime bool `json:"-" yaml:"-"` // CLI delivery routing only; false in preview
}
type HelmSourcePlan struct {
    Status, Path, Preview string
    ChangedKeys, Warnings []string
    ServerValidation, RuntimeVerification string
    Proof *HelmSourceProof `json:"-" yaml:"-"`
}
type HelmSourceProof struct { /* private input/candidate/effect/live/validation bindings */ }
func PrepareHelmSource(context.Context, HelmSourceRequest, HelmSourceDeps) (HelmSourcePlan, error)
func (HelmSourceProof) CheckFresh(context.Context, HelmSourceDeps) error

// Task 4: internal/repo/helm_write.go
// reviewOnly is assigned by existing CLI review-delivery routing, never AI/JSON.
func WriteValidatedHelmSource(context.Context, HelmSourcePlan, HelmSourceDeps, bool) (SourcePatch, error)
```

Public source `SourcePatch` gains additive sanitized `SourceStatus`, `ServerValidation`, `RuntimeVerification`, `Reconciliation` fields. `Reconciliation` is `pending`, never a rollout success. Raw final values bytes live only in the private proof. Zero/unmarshaled proofs refuse writes.

### Task 1: confined context, renderer and exact inventory

**Files:** Create repo/helm_context.go, helm_inventory.go and corresponding tests. Reuse the legacy render timeout policy without changing read-only APIs.
**Produces:** Task 1 interfaces above. Real renderer edits an isolated copy only and always cleans it up.

- [ ] Write `TestHelmContextExplicitInputsAndLastLayer`: ordered files preserved; edit target is the last explicit values layer or explicitly selected non-overridden chart defaults; missing release/mode/ns/chart, outside/symlink/URL paths and conflicting identities refuse. `TestHelmContextUnknownDeploymentInputsRefuse` covers declared unsupported overrides/post-rendering; CLI context describes operator-supplied deployment inputs, not inferred live release fidelity.
- [ ] Write `TestHelmInventoryExactIdentityAndNamespace`: same name/different namespace does not match, absent namespace uses declared render namespace, duplicate identity/anonymous/List/malformed YAML refuses, map/document order alone preserves digest, fuzzy release names do not match. Store Secret documents privately and assert public errors omit their contents.
- [ ] Write `TestHelmRendererUsesOrderedContextAndNoNetworkModes`: fake executable sees exact release/chart/ns/mode/values/kube/API arguments, no dependency-update/server/DNS/post-renderer/schema-bypass arguments. Timeout/cancellation/output-limit failure cleans copies and reports generic stage/exit details without raw stderr.
- [ ] Run `GOCACHE=/tmp/fixora-go-cache go test ./internal/repo -run 'HelmContext|HelmInventory|HelmRenderer'`; require the new tests to fail before implementation.
- [ ] Implement context capture with private immutable bytes/hashes, confined paths and renderer path/content/version identity. Bound each Helm invocation to 30s, render output to 16MiB, each values file to 1MiB and copied input bundle to 64MiB; exceeding a limit becomes review-required. Dependencies must be present locally. Reject external JSON-schema references; allow local fragments and confined bundled references. Use argv execution, not shell interpolation; no raw Helm stderr in summaries.
- [ ] Real renderer reproduces explicit install/upgrade mode, ordered values, namespace and capabilities in a private input copy. Include hooks/CRDs in inventory without executing them. Parse YAML structurally, reject aliases/duplicate keys, and retain private object copies. Pass Task 3 Scope through a HelmScopeFunc adapter (built-in fixture scopes in Task 1 tests). Inventory schema/API classification must keep cluster-scoped objects namespace-empty; unknown scope requires readable discovery or review.
- [ ] Run the focused tests and `go test ./internal/repo`; require PASS and existing read-only Helm behavior unchanged. Commit `feat(repo): capture exact Helm render inputs and inventories`.

### Task 2: workload-specific values candidates

**Files:** Create helm_candidates.go/tests; add source-delivery-only helpers in helm_valueskey.go as needed. Do not modify its legacy confidence meanings.
**Consumes:** captured input bytes, exact target inventory and the existing reviewed workload patch.
**Produces:** `HelmValuesCandidate`, `HelmMapping`, `ProposeHelmValues` above.

- [ ] Write `TestHelmCandidatesDynamicImageResourceEnvAndProbe`: synthetic charts use arbitrary key/container names; proposals include direct scalar changes and identity-preserving ConfigMap/probe map/list edits, preserve unrelated keys/comments/types and distinguish null from missing. Array identity is container/env name, not leaf-key repetition or index guesses.
- [ ] Write `TestHelmCandidatesDerivedImageAndSubchart`: candidate combinations account for current full reference and parsed registry/repository/tag/digest segments using existing image-reference helpers, plus root override paths for aliased subcharts. Do not encode common key names such as repository/tag as authoritative. A key not supported by source/value evidence does not become a guessed write.
- [ ] Write `TestHelmCandidatesRefuseUnsafeYamlAndAmbiguousLists`: aliases/merge keys/duplicate keys, unrelated credentials, ambiguous list mappings and malformed explicit JSON-pointer mappings refuse. Explicit mappings select source key paths only; they do not bypass effect checks.
- [ ] Run `GOCACHE=/tmp/fixora-go-cache go test ./internal/repo -run HelmCandidates`; observe RED.
- [ ] Implement YAML-node editing against immutable original file bytes. Generate a stable finite set from existing template/value suggestions and optional operator mappings. Maximum 32 candidate combinations; exceeding the limit returns mapping-review-required rather than truncating to the first candidate. All edits for one patch are combined before certification. Do not add a provider contract or AI key-proposal API in this checkpoint; existing AI supplies the dynamic workload patch.
- [ ] Build preview from edited paths only, with sensitive values redacted; never include unrelated surrounding source contents. Actual candidate byte buffers are private. No source writes occur here.
- [ ] Run focused and all repo tests; require PASS. Commit `feat(repo): propose dynamic Helm values edits without trusting heuristics`.

### Task 3: chart-wide effect certification and scoped validation

**Files:** Create helm_effect.go/tests; use existing fix/shadow validators, repairpolicy bindings and Kubernetes patch helpers through injected dependencies.
**Consumes/produces:** Task 3 interfaces above; no caller-supplied verified flag is accepted.

- [ ] Write `TestHelmEffectExactTargetAndUnchangedInventory`: baseline render twice, candidate render twice, exact resource set and target = strategic-merge(baseline, candidate patch); sidecar/resource/list type mismatches, unrelated Service/Secret/hook/CRD edits and additions/removals refuse. Quantity canonicalization is allowed only at Kubernetes resource quantity fields.
- [ ] Write `TestHelmEffectNondeterminismAndMultiplePassingMappings`: unstable render refuses; two different values edits that both pass require selection, not first-winner delivery. Explicit mappings must still pass full effect certification.
- [ ] Write `TestHelmEffectSourceAlreadyCorrectAndLiveDrift`: source already implements candidate => no-op outcome/no proof; relevant baseline/live container/config mismatch => source review, not runtime claim. Scope checks require exact live target UID/ns/container and preserve known API-defaulting normalization only.
- [ ] Write `TestHelmEffectValidationLevelsAndReplay`: lint/schema error, server denial/unreadable readback or relevant admission rewrite prevents a writable certificate; unavailable server checks yield pending. Source-only review cannot mint a shadow receipt. Real eligible shadows require the original bound plan/proof and matching receipt; forged/replayed/model flags refuse. Reject unsupported family and mixed fields even when rendering succeeds.
- [ ] Write `TestHelmEffectFreshnessAndSecretFreeSummaries`: changed inputs/renderer/live UID or candidate invalidate proof; serialized result loses authority; values/Secret bytes and stderr never appear in JSON/YAML/preview/provider-bound data.
- [ ] Run `GOCACHE=/tmp/fixora-go-cache go test ./internal/repo -run HelmEffect`; observe RED.
- [ ] Implement `PrepareHelmSource`: collect live snapshot, run two unchanged renders, propose candidates, apply reviewed patch with Kubernetes strategic merge, render each candidate twice and certify final combined effect. Lint only passing candidates. Treat unsupported chart behavior/ambiguous mappings as review statuses without a writable proof. Never use legacy fuzzy matches or string-normalized leaf comparisons for certification.
- [ ] Server validation returns admitted target readback in HelmValidation.Admitted through the existing kubectl/client seams; compare permitted defaulting and exact policy-relevant fields. Actual errors block writes; unavailable validation is pending. Shadow only when VerifyRuntime is requested and existing ApplyEligible/field/source-clone gates permit and applicable authority exists, with retries disabled for certified source edits. Pass source-managed review-only patches through source checks without changing ApplyEligible or manufacturing a runtime receipt.
- [ ] Create proof privately after checks, binding captured files/renderer/context, candidate bytes, both inventories, relevant live target UID/configuration and validation levels. Source-validated review plans with server validation pending can be explicitly written for review, but cannot be called server/runtime verified. Every later write rechecks these bindings.
- [ ] Run repo tests plus `go test ./internal/fix ./internal/repairpolicy ./internal/shadow`; require PASS. Commit `feat(repo): certify Helm source edits by exact render effects`.

### Task 4: guarded values-file write

**Files:** Create helm_write.go/tests; add SourcePatch summary fields in repo.go. Leave legacy WriteSourcePatch/PreviewSourcePatch and raw/Kustomize behavior intact.
**Consumes/produces:** `WriteValidatedHelmSource` and SourcePatch additions above.

- [ ] Write `TestHelmWriteExactCertifiedBytesAndUnrelatedPreservation`: only explicit selected file changes, mode/comments/unrelated keys stay; no-op/empty/zero or deserialized proof refuse; raw/Kustomize regressions stay unchanged.
- [ ] Write `TestHelmWriteFreshnessAtConfirmationAndCommitBoundary`: edits to chosen values/chart/dependency/renderer/live target between prepare/confirm/write refuse without overwriting; cancelled requests create no source change; temporary files/locks clean up on every failure. Test changed file content after successful write is detected before file-scoped commit.
- [ ] Write `TestHelmWriteReviewScopeAndPendingValidation`: unavailable server/runtime validation is labelled pending and requires explicit existing review-only delivery; it cannot become an automatic verified write. Explicit validation failure never writes, even in review mode. Quick/yes/force flags do not bypass a proof or failed check.
- [ ] Run `GOCACHE=/tmp/fixora-go-cache go test ./internal/repo -run HelmWrite`; observe RED.
- [ ] Implement private proof validation, final input/live freshness check, restrictive temporary file in destination directory, mode preservation, cooperative exclusive source-write lock, immediate original-content recheck and atomic rename. Refuse any detected conflict. This protects observed/cooperating changes; do not advertise a filesystem transaction against uncooperative writers. After write, verify exact resulting bytes and recheck before commit; no reset/revert of other actors' changes.
- [ ] Return source-written/server/runtime/reconciliation summaries truthfully; do not mark a skipped check successful or runtime receipt inferred. Existing automatic mutation eligibility and explicit review-only consent remain separate gates.
- [ ] Run repo/source regression tests; require PASS. Commit `feat(repo): guard certified Helm values writes against stale inputs`.

### Task 5: shared CLI preview/source/PR workflow and final checkpoint

**Files:** Create internal/cli/helm_source.go/tests; modify options/flag/help/routing in root.go, shared preparation in fix_prepare.go only where required, and delivery report/PR body helpers. Update docs/reference/commands.md, controller-repairs.md, remediation-coverage.md, docs/guides/delivery.md and source-and-coordinate.md. Add small source chart fixtures under test/e2e/fixtures/helm-source/ and compile/run offline fixture tests in repo (no EKS deployment).
**Interfaces:** `func prepareHelmSource(ctx context.Context, r kube.Reader, k kube.Kubectl, opts options, f analyzer.Finding, p fix.Plan) (repo.HelmSourcePlan, error)` and `func runHelmSourceDelivery(ctx context.Context, stdout, stderr io.Writer, opts options, r kube.Reader, k kube.Kubectl, f analyzer.Finding, p fix.Plan) int` in CLI. These consume Task 3–4 APIs; no second weaker preparation pipeline.

- [ ] Write `TestHelmCLIOrderedExplicitFlags`: chart/release/repeated values/edit file map into typed context. Add `--helm-render-mode install|upgrade`, `--helm-kube-version`, repeated `--helm-api-version` and optional repeated `--helm-map <rendered-json-pointer>=<values-json-pointer>` alongside the four spec flags. Explicit capabilities win; readable discovery supplies missing server version/API information, otherwise incomplete required context stays review-only. Hints locate candidates but never override conflicting explicit/live release/ns identities. Do not silently extract release values or infer a complete values chain from filenames.
- [ ] Write `TestHelmCLIPreviewNoWriteEvenWithYes`: preview and structured output expose source preview/validation levels only, with no source write, shadow creation, Git commit/push or PR. Offline preview may render/lint; live-runtime verification stays a separate explicit delivery step. Preserve existing no-context Helm diagnostic output.
- [ ] Write `TestHelmCLISourceAndPRClaimScopes`: concrete values diff precedes existing consent, source-managed direct cluster apply remains blocked, genuine policy blockers never lifted, review-only source PR title/body never says verified repair, and source/runtime/production status are distinct. An actually eligible shadow path must bind the identical rendered candidate and source proof. Server errors refuse; unavailable checks are explicitly pending review.
- [ ] Write `TestHelmCLIPRFileScopeNoOpAndRemoteFailure`: only the selected values path can commit, unrelated dirty files preserve existing refusal/preservation semantics, no-op makes no branch/PR, changed source bytes refuse commit, missing remote/push/open failures report actual stages and preserve local changes. Assert report/body excludes unrelated values/secrets/raw render content. No step invokes helm upgrade or reconciles production.
- [ ] Run `GOCACHE=/tmp/fixora-go-cache go test ./internal/cli ./internal/repo -run Helm`; observe new regressions RED. Local HTTP listeners may require escalation; no real provider key is needed.
- [ ] Implement CLI context assembly and shared Helm source routing for plain/guided/structured preview and explicit source/PR delivery. If complete new context is absent, retain the old read-only suggestions. Keep generic reviewed source proposals available; only Task 3-certified edits can call Task 4 writing. Reuse `aiCandidateStrategy`, `validateAILiveTarget` and `validateAIConfigReferences` before the existing source-review ownership branch; keep the recognized field family on a review-only plan without setting ApplyEligible. Do not parse a second weaker AI proposal. Do not route through the legacy Helm no-op and claim a PR was generated. Default output is target + file/key edits + validation/runtime/reconciliation status; edited-key diff/proof carries detail.
- [ ] Use existing PrepareBranchFiles/OpenPullRequest under current authorization rules and exact file scope. Pass certified source summaries in PR body, recheck bytes before commit, refuse no-op and preserve staged/local failure outcomes. If development work itself creates a PR, attach it using Codex attach_artifact; a Fixora test double opening a simulated URL creates no real artifact.
- [ ] Add real local Helm fixtures for arbitrary scalar keys, derived images, overridden values, aliased subchart and shared-global/hook collateral changes. Require real Helm on the implementation validation host and report those executed tests separately from fake renderer tests; ordinary portable tests may skip real Helm when absent. Secret fixtures use only synthetic values.
- [ ] Update docs with actual source coverage, flags, source-only/pending claims and manual template fallback. Analyzer status inventory changes only if actual emitted statuses/profiles change; do not relabel existing diagnostics as repaired incidents.
- [ ] Run `GOCACHE=/tmp/fixora-go-cache go test -count=1 ./...`, `go test -race -count=1 ./...`, `go vet ./...`, `make build`, `go test -c -tags='e2e,e2e_delivery' -o /tmp/fixora-helm-source-e2e.test ./test/e2e`, and `git diff --check`; require exit 0. Compile does not mean cluster execution.
- [ ] Perform one fresh whole-branch review against `cb9bd3e`, focused on Review Focus above and eligibility/source boundary. Reproduce Critical/Important findings RED→GREEN and finish with green tests. Save a durable checkpoint report including executed versus untested live surfaces. Commit only Fixora source/tests/docs, push the dedicated branch under the user's existing checkpoint workflow; do not merge or deploy EKS.

## Spec self-review and handoff

Spec sections 1–2 map to Task 1; section 3 to Task 2; render/validation/proof in sections 4–5 to Task 3; guarded writes in section 5 to Task 4; source consent/PR/output/acceptance/regression to Task 5. All shared APIs have a producing task and consumers use the same exact signatures. Each Review Focus item has refusal tests; raw buffer privacy, cancelled preview, remote failure and ambiguity are explicit.

Optional AI source-key proposals and additional step 3 policy source authorization are not part of this first values-delivery implementation; dynamic AI workload patches remain supported by shared preparation. No arbitrary template mutation or cross-resource patch expansion is implied. Source context is an operator-specified deployment model; readable hints detect known contradictions but do not prove undisclosed release inputs. Outcomes reflect that limit.

Execution remains native/inline with a fresh whole-branch reviewer at the end. This implementation plan requires the user's review before product changes start.
