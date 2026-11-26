# Sub-project 1: Client-ready UX Polish — Design

**Date:** 2026-06-26
**Status:** Approved (design); pending implementation plan
**Owner:** baka

## Context

`kubectl-fixora` already implements the full incident-remediation pipeline
(analyzer → root cause → AI explanation → fix plan → shadow validation →
apply-or-PR delivery), ~17k LOC. The goal is to make it **production grade and
easy to use for clients** who run it interactively as a local `kubectl` plugin
against their own clusters (own kubeconfig + `gh`/`glab` auth).

That overall goal was decomposed into ordered sub-projects:

- **Phase 0 — Foundation** (lightweight prerequisite): repo hygiene + commit the
  in-progress hardening work as a reviewed baseline.
- **Sub-project 1 — Client-ready UX polish** *(this document)*.
- **Sub-project 2 — Trust & safety hardening** (shadow robustness, broader
  apply-eligibility, richer PR delivery, workflow tests). *Out of scope here.*
- **Sub-project 3 — Local/GitHub file ingestion + PR** (read a local or GitHub
  file for the incident resource, patch it, update live or open a PR).
  *Out of scope here.*

This spec covers Phase 0 + Sub-project 1.

## Decisions captured during brainstorming

| Decision | Choice |
|---|---|
| WIP status (~2,773 uncommitted lines) | Mostly works; treat as baseline, review + commit in Phase 0 |
| Client runtime | Interactive local `kubectl` plugin (own kubeconfig + `gh`/`glab`) |
| Sequencing | Phase 0 → SP1 (this) → SP2 → SP3 |
| `fix` interaction model | **Staged interactive walkthrough** (4 steps, confirm at each) |
| Explanation register | **SRE peer — precise & technical** (jargon OK, no business gloss) |
| Step 1 proof/evidence | **Hidden by default**, `[w]` toggles it |
| Legacy delivery flags | **Deprecate with aliases** (backward compatible), not hard-remove |

## Current-state facts (grounded in code)

- `runGuidedFix` (`internal/cli/root.go:974`) is the text-mode `fix` path: prints
  `Why` + `Plan` + diff, writes a patch file, then branches on flags.
- Two overlapping delivery vocabularies coexist:
  - **Modern:** `--delivery {patch|cluster|pr}` + `--yes` + `--pr-base`/`--pr-title`,
    routed via `runShadowWorkflow` (`root.go:788`–931) with gates (pr requires
    `--repo` and `--yes`; Helm/GitOps blocked for `cluster`).
  - **Legacy:** `--apply` / `--source-patch` / `--gitops`, routed via a separate
    branch in `runGuidedFix` (`root.go:1003`–1077).
- Rendering helpers exist and will be reused: `termui.Why`/`Plan`/`DisplayDiff`
  (`internal/termui/termui.go:44,78`, `diff.go:41`), `openPatchEditor`,
  `writeReviewPatch`, `writeShadowFailure`, `termui.ConfirmApply`, `inputFor(opts)`.
- AI non-JSON responses currently degrade silently into a confusing hybrid
  result (`internal/ai/openai.go:276`–282).
- `--yes` and `--edit-patch` already exist (`root.go:576,578`).

## Scope

### In scope
1. Phase 0 foundation cleanup.
2. Staged interactive walkthrough for `fix`.
3. Explanation & message polish in the SRE-peer register + AI fallback fix.
4. Error messages with next-step guidance.
5. Delivery consolidation onto `--delivery`; deprecate legacy flags as aliases.
6. Help text refresh.

### Out of scope (deferred)
- Expanding which fixes are apply-eligible beyond pod-template (SP2).
- Shadow validation internals: stateful warnings, parity detail, timeouts (SP2).
- Richer PR body / reviewers / labels (SP2).
- Local/GitHub file ingestion (SP3).

## Design

### Phase 0 — Foundation (mechanical prerequisite)
- Delete stray root files: `test.go`, `test_panic.go`, `patch_tui.go`,
  `fixora-patch.yaml`.
- Add to `.gitignore`: the built `fixora-cli` binary and the default
  `fixora-patch.yaml` patch output.
- Run `make test` and `make lint`; confirm green.
- Commit the ~2,773-line WIP as a reviewed baseline checkpoint.
- **Acceptance:** clean `git status`, tests + lint pass.

### 1. Staged interactive walkthrough for `fix`

A step controller sequences four stages. It reuses existing rendering and
delivery code rather than reimplementing it. The orchestrator stays
`runGuidedFix`; the controller lives in `internal/cli` (or `internal/termui`).

- **Step 1/4 — Root cause.** Render `termui.Why` with cause only (proof hidden).
  Prompt: `[Enter] continue · [w] show proof · [q] quit`. `w` toggles the
  Proof/evidence + logs detail.
- **Step 2/4 — Proposed fix.** Show strategy, confidence, risk, and the colored
  diff (`DisplayDiff`).
  - Apply-eligible & concrete → `Validate in shadow sandbox? [Y/n]`, with
    `[e] edit patch` (reuses `openPatchEditor`).
  - Review-only / not apply-eligible → explain `BlockedReasons`, write the review
    patch (`writeReviewPatch`), and route directly to the allowed delivery
    options (pr/file), making clear shadow + direct apply are blocked and why.
- **Step 3/4 — Shadow validation.** Run `runShadowWorkflow` with staged progress
  (clone → NetworkPolicy → readiness → parity). On pass: show parity summary.
  On fail: show `writeShadowFailure` output and offer retry (existing retry
  provider) or fall back to file/PR.
- **Step 4/4 — Deliver.** One prompt mapping onto existing `--delivery` values:
  ```
  How do you want to ship this fix?
   1) Apply directly to the cluster
   2) Open a GitHub/GitLab PR
   3) Write patch file only  (default)
  ```
  Honors current gates: option 1 blocked for Helm/GitOps-managed resources
  (`sourceManaged`) with explanation; option 2 requires `--repo` (prompt for the
  path if absent).

**Modes & fallbacks:**
- Walkthrough runs only when stdout is a TTY **and** `-o text`.
- `-o json|yaml|...` → unchanged non-interactive `output.Write(plan)`.
- Non-TTY or `--yes`/non-interactive → no prompts; delivery chosen by flags
  (`--delivery` / default file); preserves scriptability.
- Explicit `--delivery`, `--no-shadow`, etc. pre-answer the relevant step (skip
  that prompt) while still showing the stage output.
- Input read via existing `inputFor(opts)` so tests inject
  `bufio.NewReader(strings.NewReader(...))` per the repo convention.

### 2. Explanation & message polish (SRE-peer register)
- **Tone contract:** precise, technical, concise, confident; correct Kubernetes
  terms; names the specific resource/field; no business-impact gloss; no filler.
- **AI prompt:** extend `jsonContract()` / system prompt in `openai.go` with this
  style guidance plus an explicit "never invent secret or config values."
- **AI silent-fallback fix** (`openai.go:276`–282): on a non-JSON response, emit a
  visible warning (`AI response could not be parsed; using deterministic plan`)
  and use the deterministic plan instead of presenting a hybrid pseudo-result.
- **Deterministic templates** (`planner.go`): make templated recommendation
  strings incident-specific where data allows (name the real container/field);
  keep internal `TODO_*` placeholders out of user-facing copy (or clearly label
  them as fields to fill in).

### 3. Error messages with next-step guidance
Standardize on: `error: <what went wrong>` followed, when actionable, by a
`Next: <exact command or fix>` line. Introduce a small helper
`fail(stderr, msg, next)` for consistency. Apply to the known terse spots:
- shadow requires apply-eligible patch (`root.go:790`)
- `--delivery=pr requires --repo` / `--yes` (`root.go:807,811`)
- legacy `--gitops/--source-patch requires --repo` (`root.go:1005,1042`)
- shadow verification failed → point to retry / file-only / pr.

Machine-readable exit codes are unchanged.

### 4. Delivery consolidation & flag cleanup
- Canonical selector: the existing `--delivery {patch|cluster|pr}`.
- Unify the legacy `runGuidedFix` delivery branch so all delivery flows through a
  single code path (the `--delivery` switch), eliminating the parallel vocabulary.
- Deprecated aliases (still functional, print a one-line stderr notice):
  - `--apply` → `--delivery cluster`
  - `--source-patch` → `--delivery pr`
  - `--gitops` → `--delivery pr`
- `--repo <path>` remains required for `pr`. Document that `pr` is the path
  GitOps/Helm shops use; Helm direct `cluster` apply stays blocked.
- In interactive mode, Step 4 is the delivery selector; flags pre-select and skip
  the prompt.

### 5. Help text refresh
Update `printHelp` (`root.go:2376`) to: lead with the guided `fix` flow, document
`--delivery`, `--yes`, and the interactive-vs-scripted behavior. Keep the existing
fast-workflow vs specialist segmentation.

## Testing

- **Walkthrough** (injected stdin): apply path, pr path, file-only, quit at
  Step 1, edit-patch, review-only (non-apply-eligible) routing, non-TTY fallback,
  `-o json` bypass.
- **AI fallback:** non-JSON response emits a warning and uses the deterministic
  plan.
- **Errors:** assert `Next:` hints present at each known error spot.
- **Flags:** `--delivery` parsing, deprecated-alias mapping, and that `--gitops`/
  `--source-patch`/`--apply` still work with a deprecation notice.

## Acceptance criteria

- A client can run `kubectl fixora fix <resource>` and be guided through
  cause → fix → shadow → deliver with confirmations, in precise SRE language.
- Proof/evidence is hidden by default and shown via `[w]`.
- `-o json` and non-TTY behavior remain unchanged and scriptable.
- Every known terse error now includes an actionable `Next:` step.
- `--delivery` is the documented canonical selector; legacy flags still work with
  a deprecation notice.
- Stray files removed; tree clean; `make test` and `make lint` pass.
