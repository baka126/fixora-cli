# Fixora Documentation Website Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the single-page documentation with a polished, accurate, release-first GitHub Pages website covering Fixora's use cases, workflows, commands, and safety boundaries.

**Architecture:** Keep Jekyll in `docs/`. Use one shared layout, data-driven navigation, local CSS and a small optional search script. Write task-oriented Markdown pages and a concise README. A local standard-library validator checks source pages and generated links; GitHub Actions builds pull requests and deploys only from `main`.

**Tech Stack:** Jekyll/GitHub Pages, Liquid, Markdown, CSS, small vanilla JavaScript, Python 3 standard library, GitHub Actions.

**Spec:** [Documentation website design](../specs/2026-09-26-documentation-website-design.md)

## Global Constraints

- Use `v0.8.0` as the published-release baseline. Compare every example and capability claim against the tag, current command help/registry, and tests. Mark features available only on `codex/production-hardening` as **Upcoming**, link to that branch, and keep them out of release quick-start examples.
- State side effects beside commands: cluster reads, shadow resource creation, local writes, cluster mutation, or repository/PR writes. Never imply AI certainty or verification when skipped.
- Keep reading, navigation, and installation instructions usable without JavaScript. Use `relative_url` for local links/assets and `/fixora-cli` as the production base path.
- Exclude `docs/superpowers/` and generated files from the published site. Avoid external fonts, trackers, and runtime services.
- Preserve the existing main-only deployment gate; PRs may build and validate but must not publish.
- Apply the test steps to actual behavior and links. Do not add content assertions that merely restate prose.

## File structure

| Path | Responsibility |
| --- | --- |
| `docs/_config.yml` | Site identity, URL/base path, collections/exclusions, metadata defaults. |
| `docs/_data/navigation.yml` | Shared top/sidebar navigation with release-aware labels. |
| `docs/_layouts/default.html` | Semantic shell, skip link, navigation, breadcrumbs, metadata, footer. |
| `docs/_includes/` | Reusable status, command-consequence, and related-link components where repetition justifies them. |
| `docs/assets/css/site.css` | Responsive visual system, code blocks, focus, print, reduced motion. |
| `docs/assets/js/search.js` | Optional local title/summary search enhancement; no required navigation behavior. |
| `docs/index.md`, `docs/get-started.md`, `docs/use-cases/*.md`, `docs/guides/*.md`, `docs/reference/*.md`, `docs/safety.md` | Public content. |
| `docs/checklist.md` | Remove stale public checklist or rewrite as a dated maintainer document excluded from publishing. |
| `README.md` | Short project entry point, release quick start, and links to site. |
| `scripts/check_docs.py`, `tests/test_check_docs.py` | Source and generated-site validation; fixture-based tests for links and navigation. |
| `.github/workflows/pages.yml` | Main-only deployment, with build/link validation. |
| `.github/workflows/docs.yml` | PR build and validation, without deployment credentials. |

## Review Focus

1. A branch-only feature in a release page must carry an Upcoming label; Task 3 tests that the layout renders the label from page metadata.
2. A command that creates shadow resources or writes patches must expose its consequence beside the example; Task 3 tests that the command include renders the supplied consequence.
3. Links and assets must resolve under `/fixora-cli`; Tasks 2 and 6 test generated URLs and broken paths.
4. A missing command, analyzer, or symptom guide must fail inventory coverage; Tasks 4 and 5 test missing entries.
5. Mobile and keyboard visitors must reach every main section without JavaScript; Tasks 2 and 6 inspect rendered navigation with scripts disabled.

---

## Task 1: Establish a release inventory and content map

**Files:** `docs/_data/feature_status.yml` (new), `docs/_data/navigation.yml` (new), `docs/_config.yml`, `scripts/check_docs.py` (new), `tests/test_check_docs.py` (new).

**Interfaces:** `feature_status.yml` names each documented feature, its release status (`released` or `upcoming`), source evidence, and page owner. Navigation entries contain title, path, summary, and section. `scripts/check_docs.py` exposes `check_source(source: Path) -> list[str]` and later `check_site(site: Path, baseurl: str) -> list[str]`; CLI exits nonzero when either returns errors. Keep data human-reviewable; it is the audit checklist for content authors.

- [ ] Capture `git show v0.8.0:<path>`, `go run ./cmd/kubectl-fixora help --advanced`, and `go run ./cmd/kubectl-fixora filters -o json`; record release-versus-branch differences for primary commands, AI, MCP, shadow/delivery, analyzers, and UX.
- [ ] Write `test_duplicate_navigation_path` and `test_missing_feature_owner` fixtures with expected `check_source` errors.
- [ ] Run `python3 -m unittest tests.test_check_docs`; confirm both new tests fail because the checker is absent.
- [ ] Add inventory/navigation data and implement `check_source(source: Path) -> list[str]` in `scripts/check_docs.py`.
- [ ] Run unit tests and `python3 scripts/check_docs.py --source docs`; confirm zero errors.
- [ ] Review inventory against `v0.8.0` and branch help, including aliases/deprecated flags; commit this task.

## Task 2: Build the Jekyll shell and home page

**Files:** `docs/_config.yml`, `docs/_layouts/default.html` (new), `docs/_includes/*` (as needed), `docs/assets/css/site.css` (new), `docs/assets/js/search.js` (new), `docs/index.md`, `tests/test_check_docs.py`.

**Interfaces:** Every public page has front matter `title`, `description`, `permalink`, and `section`; branch-only pages also set `status: upcoming`. Layout renders canonical URL and page metadata, top navigation, active section, breadcrumb, accessible mobile menu, optional local search, related links, and version/source footer. A command callout receives `command` and `consequence`. The home page links to getting started and symptom guides directly.

- [ ] Add `test_missing_front_matter`, `test_missing_nav_target`, and `test_duplicate_permalink` fixtures with expected errors; run them and confirm failure.
- [ ] Replace Cayman settings with the configured site URL/base path and exclusions; create the layout and responsive stylesheet. Use native link/menu behavior so navigation works with scripts disabled.
- [ ] Write the new home page with a labeled illustrative terminal flow, installation/get-started entry, use-case cards, and clear AI/shadow boundaries. Add optional local search only after core links work.
- [ ] Run validator tests and a Jekyll build through the project workflow or local Jekyll if available; inspect generated links for `/fixora-cli` prefix and metadata. Confirm a browser can follow primary navigation with JavaScript disabled. Commit shell/home.

## Task 3: Write release-safe onboarding and incident workflow

**Files:** `docs/get-started.md`, `docs/guides/incident-workflow.md`, `docs/guides/delivery.md`, `docs/guides/source-and-coordinate.md`, `docs/_data/navigation.yml`, `docs/_data/feature_status.yml`.

**Interfaces:** The first scan is copyable with the released binary and least-privilege access. Workflow examples distinguish `scan`, `why`, `fix`, review-only plans, shadow verification, local patch, cluster apply, and PR/source delivery; branch-only examples carry visible Upcoming status and branch prerequisites.

- [ ] Add fixtures proving `status: upcoming` renders a visible badge and the command include renders a supplied write consequence; run checks and confirm failure.
- [ ] Write onboarding from installation through context, RBAC, scan, explanation, and preview, verifying each command and flag against the release tag. Include the action/side-effect label near every example.
- [ ] Write the incident, source/coordinate, and delivery guides from CLI help plus implementation/tests; document proof/confidence, skipped verification, post-apply health checks, rollback, and review-only outcomes.
- [ ] Run source validator and Jekyll build/link check; manually compare all code blocks with `v0.8.0` or Upcoming branch help. Commit guides.

## Task 4: Cover symptom-oriented use cases

**Files:** `docs/use-cases/index.md`, `docs/use-cases/workloads.md`, `docs/use-cases/networking.md`, `docs/use-cases/storage-security.md`, `docs/use-cases/delivery-operations.md`, navigation/status data.

**Interfaces:** Each symptom section states resource/signal inspected, copyable command, side effect, possible diagnosis or next step, and limitation. The hub links to every section in no more than one extra click from home.

- [ ] Add a fixture-based link test for cross-page fragments and a content coverage checklist in the feature inventory; run the test to see missing targets.
- [ ] Write crash loops/probes, image pulls/architecture, OOM, Pending/scheduling, Jobs/CronJobs, and node pressure sections.
- [ ] Write Service/Gateway/Ingress routing, DNS, PVC/storage, RBAC/security policy, and Helm/GitOps sections; use status labels for branch-only analyzers or commands.
- [ ] Run source and generated-link checks; review examples against analyzer registry and tests, then commit use cases.

## Task 5: Cover advanced capabilities, reference, and safety

**Files:** `docs/guides/ai.md`, `docs/guides/mcp.md`, `docs/guides/dashboards-and-integrations.md`, `docs/guides/reports-cache-serve.md`, `docs/reference/commands.md`, `docs/reference/analyzers.md`, `docs/reference/configuration-and-output.md`, `docs/safety.md`, navigation/status data.

**Interfaces:** Reference accounts for every `help --advanced` command family and alias, grouped flags and deprecations, analyzer defaults, configuration precedence, output formats, exit behavior. Guides explain AI provider setup/data flow, MCP tool scope, optional integrations/custom analyzers, reports/bundles/cache/local HTTP, dashboards, and shadow lifecycle. Safety page gives read/write and permission boundaries.

- [ ] Add a check that compares command headings/identifiers in reference metadata with command inventory captured from current and release help; see a deliberate missing-command fixture fail.
- [ ] Write AI, MCP, dashboards, integrations, reports, cache, and serve guides with release/upcoming status. Verify sensitive-data and network claims in code.
- [ ] Write grouped command, analyzer, config/output reference and safety page. Include examples for aliases/deprecated flags and exact verification/delivery consequences.
- [ ] Run unit tests, source validator, Jekyll build/link check, and manually compare reference coverage with both help/registry outputs; commit reference and safety docs.

## Task 6: Publishing gates, README, and rendered review

**Files:** `.github/workflows/docs.yml` (new), `.github/workflows/pages.yml`, `README.md`, `docs/checklist.md` (remove/rewrite), `scripts/check_docs.py`, `tests/test_check_docs.py`.

**Interfaces:** Pull requests build the site and fail on missing front matter, navigation targets, or internal links/fragments, without Pages deployment permissions. `main` builds, validates, and deploys the same output. README links into public site and keeps a release-safe quick start.

- [ ] Add failing fixture tests for relative links under `/fixora-cli`, excluded maintainer pages, and broken generated asset paths; run them to confirm failures.
- [ ] Wire the Python validator and Jekyll build into a PR workflow. Keep deploy only in the existing main workflow and add validation before upload. Remove or rewrite the stale checklist.
- [ ] Rewrite README as concise entry point; verify badge/site links, install methods, and quick-start against released `v0.8.0`.
- [ ] Run `python3 -m unittest tests.test_check_docs`, `python3 scripts/check_docs.py --source docs --site _site`, and a Jekyll build. Inspect rendered pages at phone/tablet/desktop widths, keyboard focus/menu, contrast, overflow, assets, and with JavaScript disabled. Fix defects and rerun affected checks.
- [ ] Review the five Review Focus risks and every acceptance criterion in the spec; run final Go/help checks needed to validate examples, record any environment-limited checks, and commit the verified site. Keep the branch unmerged until reviewed.
