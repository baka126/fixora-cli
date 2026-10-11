# Step 4: validated Helm source delivery

Status: proposed design for user review. Branch: `codex/helm-source-delivery`, based on step 3 checkpoint `cb9bd3e`.

## Intent and success

Fixora must turn a workload-specific repair into a durable Helm source change rather than a rendered-object patch that reconciliation overwrites. Preserve the working common repairs, provider contracts and step 3 authority/verification gates. AI may propose workload-specific changes; neither confidence nor chart-key heuristics establish that a source edit implements the intended repair.

Success for this checkpoint: common repairs on a local chart can produce an exact values diff, demonstrate that rendering applies only the intended workload change, validate it, and deliver the selected source file through the existing local/PR workflow. Ambiguous or unsupported templates still produce an actionable source-review outcome. A source PR is not reported as a healthy production rollout.

## Existing implementation and gap

`internal/repo/helm_source.go` locates candidate chart/template ownership; `helm_render.go` compares a proposed patch with a default render; `helm_valueskey.go` suggests keys using template references and value matches. These are diagnostic suggestions. Current source lookup uses permissive release-name matches and does not pass ordered values or namespace to the renderer. Current `WriteSourcePatch` returns from the Helm branch without changing source.

The new delivery path must not treat these suggestions, default renders or legacy pinpointed flags as authorization. The current read-only helper behavior remains available for review when delivery context is incomplete.

## Approaches considered

1. **Recommended: values candidates certified by full render comparison.** Reuse heuristic suggestions as candidates, edit an explicit values file in an isolated copy, and require exact semantic render effects. This keeps application-specific inference dynamic while making acceptance deterministic.
2. **Heuristic values writes.** Simpler but repeated keys, subchart aliases, derived images and shared globals can alter unrelated workloads. Rejected as an automatic delivery path.
3. **General template rewriting.** Reaches more charts but introduces arbitrary Go-template logic and untested render branches. Keep template location and manual proposals available; automatic template rewriting is outside the first implementation checkpoint.

## 1. Explicit render/source context

Add a typed Helm render context and an additive source-delivery API; leave raw/Kustomize behavior intact. Context includes an explicitly selected local chart, release, namespace, ordered local values files, selected editable values file, install/upgrade mode, Kubernetes version and API capabilities when the chart needs them. Helm CLI identity/version and every input fingerprint are part of validation evidence.

Proposed CLI flags: `--helm-chart`, `--helm-release`, repeated `--helm-values`, and `--helm-edit-values`. Namespace uses existing `-n`. Capabilities and upgrade context are collected from available cluster/release metadata or explicitly supplied where discovery is unavailable; no assertion that defaults reproduce a release. Values filenames found by walking the repo are not an ordered deployment configuration.

The editable file must be an explicit source input, within the selected repository, and the last effective user values layer; editing chart defaults is allowed only when explicitly selected and not overridden by supplied layers. Refuse missing release identity, conflicting live release/namespace hints, ambiguous charts, unreadable inputs, unknown higher-precedence overrides, external/URL inputs, symlink escape or path traversal. Values and source paths never come from an AI response.

Initially support local, already-resolved chart dependencies and local values YAML. No dependency downloads, Helm upgrades, post-renderer execution, DNS lookups, schema-validation bypass, external schema fetches or automatic release-values extraction. Unsupported deployment inputs such as set/set-file overrides, encrypted values and post-renderers require source review until represented faithfully.

## 2. Exact render inventory and baseline

Run bounded, cancellable `helm template` with the complete context. Include the entire render inventory, including hooks and CRDs for comparison, without executing them. Parse YAML documents structurally and identify resources by API version, kind, exact name and effective namespace. Require a unique target; do not authorize fuzzy release-prefix/suffix matches.

Render the unchanged source twice. Require the inventories to agree; random/time/lookup-dependent output cannot certify a durable source change. Inventory matching is order-independent but rejects duplicate resource identities, anonymous resources, unexpected document shapes and malformed YAML. Do not use the existing type-forgiving string normalization as semantic equality.

The baseline target must agree with the diagnosed live configuration relevant to the supported repair policy after known Kubernetes defaulting normalization. If source already renders the intended repair, report source-already-correct and identify live drift; do not create a no-op PR. If the baseline does not represent the failing configuration, return source review rather than attaching a live repair claim to another revision.

## 3. Dynamic values candidate generation

First implement supported common field families: image, resource requests/limits, ConfigMap env references and probe settings. Preserve container and resource identity. Values-backed startup/placement/security candidates may be added through the same certifier only with the original step 3 operator authority and family proof; those policies gain no new authority from a values key.

Use existing template/value suggestions to propose a finite set of candidates. An optional AI source-key proposal uses the existing provider contract and sanitized chart/key context; it does not send raw values files, Secrets, arbitrary chart file contents or credentials. No workload names, images, ports, resource values or chart-specific key dictionaries are encoded in decisions.

Candidate edits use YAML nodes to preserve unrelated keys and comments. Preserve value types; treat null/deletion and sequence replacement according to Helm merge semantics. Initially handle explicit map/scalar values edits and identity-preserving supported list edits; YAML aliases/merge keys, duplicate keys and ambiguous list/key mappings refuse automatic writing. Multiple source changes must be validated together as the final candidate, never compose independently passing tests without a final render.

Do not accept template-line or value-match confidence alone. Derived repository/tag images, `include`, `tpl`, subchart aliases and shared global keys are acceptable only if a proposed values edit passes exact render equivalence. Otherwise report the owning template and missing mapping information for manual review. If several distinct edits satisfy the same effect, require the operator to choose; do not silently select the first file/key.

## 4. Render equivalence and Kubernetes validation

Construct the expected target by applying the reviewed workload patch to the baseline target using Kubernetes strategic-merge semantics. Named containers/env/mount lists, null deletions, command/args arrays and resource quantities must retain their actual semantics; container position is not identity. Preserve the existing candidate allowlist and live container/configuration checks.

Render the candidate chart twice in an isolated source copy. Require the exact same resource identity set and every non-target resource to be semantically unchanged. The target must equal baseline plus the exact intended patch, without changes to unrelated fields. Resource quantities may canonicalize; strings, numbers, booleans, arrays and map values are otherwise type-sensitive. No unreviewed changes to Services, RBAC, Secrets, hooks, CRDs or other workloads. Shared values that affect another resource are refused, even when the target looks correct.

Run chart/schema validation with checks enabled. Server dry-run the proposed target when cluster validation is available; errors, unreadable admission results and mutation that changes policy-relevant fields block validated delivery. Offline render success is reported separately from server validation and runtime verification. Do not server-apply the full chart.

Where existing repair eligibility and source-clone restrictions permit, verify the exact rendered target candidate through the existing shadow engine. Reuse the applicable family authority and bind verification to the source/render context. Retries cannot silently revise the already-certified values edit. Secret/PVC/privileged/host sources remain restricted. A chart-render check cannot establish application readiness or invent runtime authority.

Do not globally turn Helm review-only AI plans into apply-eligible plans. A candidate blocked only by source ownership may be evaluated for an explicitly selected source-review delivery; other policy blockers remain blockers. Such a proposal is labelled source-validated, not runtime-verified, unless all existing runtime eligibility and shadow gates pass. Preserve `fix.Plan.ApplyEligible` as the automatic mutation gate and all direct Helm/GitOps delivery blocks.

## 5. Source delivery and evidence freshness

Use an opaque in-memory source proof binding the repair candidate, target/release/context, original input hashes, final edited file bytes, complete before/after render hashes and validation outcomes. Serialization, model result flags and existing suggestion confidence cannot recreate it. Keep sanitized summaries in normal output; raw values, full renders and baseline authority are excluded from provider prompts and reports.

Before writing, recheck all source input fingerprints and the live target identity/configuration required by the selected claim. Re-render if validation inputs change. Refuse stale chart/dependency/values/template files. No write is allowed during preview or on failed validation; temporary copies/artifacts are cleaned up on failure/cancellation.

The certified bytes replace only the explicit selected source file, atomically, with a fresh-content guard. Preserve unrelated local changes and source file permissions. Show the values diff and concise effect summary before existing source/PR confirmation. Automatic source delivery requires the bound validated candidate and the applicable existing eligibility/consent gate; a review-only suggestion cannot silently become a verified PR.

Use the existing file-scoped branch/PR workflow, include only changed source files, and reject no-op diffs. Recheck before commit if files change after writing. A remote failure reports local-written/committed/pushed/PR-opened states truthfully; it does not discard the user's source edits or reset their checkout. No PR publishing occurs merely because preview/render passed.

Direct cluster delivery, `helm upgrade`, reconciliation triggering and automatic rollback of releases stay outside this step. PR rollback guidance is a source revert using the normal deployment process. Report source validation, optional runtime shadow verification and pending reconciliation separately; do not claim that opening a PR repaired EKS.

## 6. User-facing outcomes

Default text stays concise: target, values file/key edits, render scope result, validation/runtime status and next action. Source/render diffs and caveats are available in preview/proof/structured output.

Outcomes: mapping-review-required, source-already-correct, render-effect-mismatch, source-validated, server-validation-pending/failed, shadow-verified when actually earned, source-written, PR-opened and reconciliation-pending. Missing context identifies the specific missing input without printing values or keys containing secrets.

Example eventual workflow:

```sh
kubectl fixora fix Deployment/api -n application --ai --preview \
  --repo /path/to/repo --helm-chart charts/application \
  --helm-release application --helm-values deploy/common.yaml \
  --helm-values deploy/production.yaml --helm-edit-values deploy/production.yaml
```

The same source inputs feed explicit source/PR delivery after required checks. This example is a proposed interface, not a currently available command.

## Acceptance matrix

Positive: dynamic image/probe/resource/ConfigMap values edits; release/namespace-specific names; ordered overrides; uniquely certified derived image keys; subchart aliases with root values overrides; null/deletion; quantity canonicalization; comment preservation; source-already-correct; source-only and runtime-verified claims kept distinct.

Refusal: fuzzy or duplicate target, wrong namespace/release, hidden overrides, repeated-key ambiguity, multiple passing mappings, changed shared/global side effects, resource additions/removals, nondeterministic/hook output, unsupported templates, schema/server validation failures, admission rewrites, Secret/credential exposure, source path escape, concurrent edits, stale/replayed proof, quick/force bypass, no-op PR and cancelled preview/delivery.

Regression: all step 1–3 common repairs/controller policies/provider tests remain green; raw/Kustomize delivery and review-only source proposals retain their current behavior. Fake renderer/validation/PR seams cover failure paths. Real local Helm fixture renders cover precedence, derived fields, subcharts and whole-inventory comparison. No AI credits or EKS mutation are necessary for unit validation.

Final checks: full unit/race suites, vet, build, E2E compilation, fresh branch review and documentation/inventory updates. Optional live EKS source validation uses a disposable namespace only with separate explicit deployment intent; it is not required to prove the offline source pipeline. Do not commit local infrastructure/state/demo files.

## References checked 2026-10-10

- [Helm template](https://helm.sh/docs/helm/helm_template/): render flags and distinction between local rendering and server validation.
- [Helm upgrade](https://helm.sh/docs/helm/helm_upgrade/): values precedence and release semantics; this design does not invoke upgrade.
- [Helm charts](https://helm.sh/docs/topics/charts/): chart/subchart values and values schema validation.

## Self-review

No unconditional trust is granted by a matching heuristic key, model confidence or render success. Render-only, server validation, runtime shadow and production reconciliation are separate outcomes. The initial automated scope is values edits; arbitrary template rewriting and cross-resource changes remain review. Existing field policies and direct source-ownership mutation restrictions remain intact.
