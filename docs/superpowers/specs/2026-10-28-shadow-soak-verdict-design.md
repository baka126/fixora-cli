# Shadow Soak Verdict Design

## Goal

Make shadow verification reject short-lived readiness and report timeouts without calling them workload failures.

## Decisions

- For long-running workloads, require Ready within 60 seconds of Pod creation, then continuously Running and Ready for a 120-second stability window.
- Keep Job/CronJob as a no-soak exception, but require the cloned Pod to reach `Succeeded`; Running and Ready alone do not prove batch completion.
- Any container restart from initial observation through the end of the stability window fails verification. Readiness regression, leaving Running, terminal failure, and watch closure also fail.
- If the verification deadline expires before the required phases finish, return `inconclusive`, not `failed`; do not retry an inconclusive observation as though it proved a bad patch.
- Keep `--shadow-timeout` as the upper bound for the verification attempt. Separate fix plan preparation's existing analysis timeout from the subsequent shadow run so analysis time does not silently consume the verification budget. Parent cancellation must still propagate.
- Liveness and startup-probe timing is bounded by a conservative estimate of the first failed-probe threshold, including initial delay, period, failure threshold, and timeout.
- No new user-facing phase-duration flags in this MVP slice.

## Scope

Implement phase-aware watch verification, status mapping, time budget separation for the guided fix workflow, focused tests, user documentation, and the phase progress ledger. No cluster-level E2E is run unless a local Kubernetes runtime is available.
