# Fixora CLI documentation website design

## Intent and audience

Rebuild the existing GitHub Pages documentation into a professional public website for operators who are evaluating Fixora or using it during a Kubernetes incident. A first-time visitor should understand what Fixora does, what it can change, how verification works, and how to complete a first scan. A returning user should find an exact command, flag, analyzer, or troubleshooting path quickly. Platform engineers and security reviewers need an accurate account of RBAC, data sent to AI providers, shadow resources, delivery gates, and rollback behavior.

The site must describe real CLI behavior. It must not imply that AI diagnoses are guaranteed, that shadow parity proves a production rollout will succeed, or that a patch was verified when verification was skipped. There is no hosted Fixora service in this repository.

## Platform choice

Keep the existing Jekyll site in `docs/` and the GitHub Pages deployment workflow. Replace the Cayman presentation with a local layout, stylesheet, and minimal client-side behavior. GitHub Pages already builds `docs/` from `main`; preserving that route avoids a new package runtime or hosting migration. The site must work with JavaScript disabled except for optional search and menu enhancement.

Alternatives considered:

1. **VitePress or Docusaurus:** richer built-in navigation and search, but a new Node toolchain, dependency maintenance, and deployment rewrite for a mostly static reference site.
2. **A larger README or one Jekyll page:** cheapest to maintain, but weak navigation and poor separation between evaluation, task guides, and reference. The current page has already become hard to scan and contains stale claims.

## Information architecture

Use a consistent top navigation: **Overview**, **Get started**, **Use cases**, **Guides**, **Reference**, **Safety**. Add a compact search that indexes titles and summaries locally, without an external service. Each page has a visible title, short purpose, breadcrumb or section context, table of contents where helpful, and links to related tasks. Mobile navigation must remain keyboard accessible.

| Area | Content and outcome |
| --- | --- |
| Home | Clear value statement; three-step incident flow (`scan`, `why`, `fix`); representative terminal output; AI and shadow verification boundaries; use-case cards; install and documentation calls to action. |
| Get started | Prerequisites, supported installation paths, kubeconfig/context selection, least-privilege checks, first scan, first explanation, preview-only fix, and where to go next. |
| Use cases | Symptom-oriented guides for crash loops/probes, image pulls and architecture mismatch, OOM/resource pressure, Pending/scheduling, Service/Gateway/Ingress routing, DNS, PVC/storage, RBAC/security policy, Jobs/CronJobs, node pressure, and Helm/GitOps-managed workloads. Each states what evidence is checked, example commands, possible outcome, and limits. |
| Incident workflow | `scan → why → fix`; interactive and scripted modes; proof and confidence; concrete patch inputs; review-only states; shadow verification; local patch, cluster, and PR delivery; post-apply health checks and rollback. |
| Advanced guides | `coordinate`, source validation/preflight, AI providers and configuration, MCP setup/tool scope, dashboards, integrations/custom analyzers, reports/bundles, cache, and local HTTP serve mode. |
| Reference | Every supported command and alias, global and workflow-specific flags, analyzer catalog and defaults, configuration precedence, output formats and exit behavior. Group by task rather than one undifferentiated list. |
| Safety and operations | Required reads and optional writes, redaction, Secret handling, AI data flow, shadow resource lifecycle, GitOps/source restrictions, production permission model, troubleshooting, and limitations. |

Retire or rewrite the stale `docs/checklist.md`; it must not remain a public claim that MCP or typed reads are absent when the code implements them. The README becomes a concise project entry point linking to the site and retaining install, quick start, and development basics. Avoid copying whole reference sections into both places.

## Version and claim policy

The public site is release-first. Commands available in the latest published release are described as current. Features present only on the pushed `codex/production-hardening` branch are visibly marked **Upcoming** with a source branch link, and are excluded from release install examples until released. A release update removes those labels only after the release artifact includes the behavior. The page footer records the documentation version or source revision.

Before writing a claim or example, compare it with command routing, `kubectl fixora help --advanced`, analyzer registry output, tests, and release-tag source. In particular, distinguish `doctor` (cluster checks) from `ai doctor`, note that bare invocation offers a terminal chooser only on the newer branch, and distinguish shadow-verified from `--quick` output. Examples must state whether they read the cluster, create shadow resources, change the cluster, write a local file, or push/open a PR. Do not present optional integration detection as a guaranteed remediation.

## Visual and interaction design

Use a restrained operator-tool identity: dark navy and blue-green accents with high-contrast light content surfaces, clear status colors, readable code blocks, and generous spacing. A terminal-style workflow illustration on the home page may show a **labeled example** rather than invented live results. Avoid stock cluster imagery, large animation, external fonts, trackers, and heavyweight JavaScript. Use responsive cards and a documentation sidebar on wide screens; stack navigation and content on small screens. Ensure semantic headings, focus visibility, descriptive link text, code block scrolling, and reduced-motion support. Target WCAG AA text contrast.

The CSS and optional search index are local assets. Use Jekyll's `relative_url` handling and a configured `/fixora-cli` base path so navigation works on GitHub Pages and in local builds. Metadata includes page titles, descriptions, canonical URLs, and social preview defaults.

## Publishing and quality gates

Keep deployment on `main`. A documentation change on a branch or pull request must build the Jekyll site and check internal links before merge; it must not deploy. The existing Pages workflow continues to deploy reviewed `main` changes. Exclude `docs/superpowers/` and other maintainer-only working files from the rendered site. Add lightweight checks for missing front matter/navigation entries and broken local links. Validate example commands against supported command names and flags where practical without requiring a live cluster; cluster-dependent examples are reviewed against tests and source.

Before release, inspect rendered pages at phone, tablet, and desktop widths; check keyboard navigation, focus order, contrast, overflow, broken assets, and no-JavaScript navigation. Review all pages for obsolete commands, unqualified safety claims, and cross-page contradictions. Build and link checks must pass. The site must load without a cloud API, and core reading/navigation must not depend on JavaScript.

## Acceptance criteria

- A visitor can reach installation and a safe first scan from the home page in one click, and a symptom guide in at most two clicks.
- The command reference covers the supported `help --advanced` command families and identifies aliases and deprecated flags.
- Use-case guides cover the incident classes listed above, with copyable commands and explicit read/write/verification consequences.
- AI, MCP, shadow, PR, and cluster delivery pages describe their boundaries accurately for the documented version.
- The README, site, and CLI help agree on the primary commands and release status.
- The site renders responsively and passes the build, internal-link, and accessibility review described above.
- No documentation branch push publishes the public site; publication occurs only through the existing `main` Pages workflow after review.
