# Scenario fixtures

Failure manifests copied from https://github.com/baka126/fixora-demo and
transformed for CI. The demo repo is the human-facing demo; this is a stable
snapshot the e2e scenario suites run against.

## Transforms applied to every file

- Removed all `fixora.io/repo-*` annotations (CI has no repo to patch; they
  only matter for `fix --delivery pr`).
- Removed `namespace: default` — each test injects its own per-test namespace.
- Deployment renamed to `<scenario>-demo`; `spec.selector.matchLabels.app` and
  `spec.template.metadata.labels.app` both set to the same, so the harness can
  poll pods with `-l app=<scenario>-demo`.
- Deterministic-set images (imagepull, crashloop, missing-config, pending,
  dependency) pinned to `public.ecr.aws/docker/library/busybox:1.36`;
  security uses `public.ecr.aws/docker/library/alpine:3.20`. These pull fast
  and reliably on kind.
- oomkilled keeps `polinux/stress` and probe keeps `python:3.9-slim` — they
  need the real runtime to reach their failure state; only the non-blocking
  delivery job runs them.

## Re-syncing

If a scenario in fixora-demo changes materially, update the corresponding file
here and note the new source commit in the next line.

Source: fixora-demo main branch, copied 2026-08-31.
