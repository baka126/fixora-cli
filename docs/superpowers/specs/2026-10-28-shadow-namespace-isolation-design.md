# Shadow Namespace Isolation Design

## Goal

Run Fixora shadow verification in a fresh namespace that cannot access production dependencies, and report a pass only when the isolation gate and candidate verification are both conclusive.

## Decisions

- Create a uniquely named namespace for each attempt, labeled with a Fixora run ID and source namespace. Apply default-deny ingress and egress before creating any candidate pod.
- Prove standard NetworkPolicy enforcement before candidate creation using disposable witness pods: first prove TCP connectivity in both directions without policy, then prove both directions fail after the sandbox policy is active. A failed/indeterminate control, active unsupported cluster-level networking policy, incomplete policy inventory, missing permission, or unavailable witness image fails closed.
- Discover every nonstandard NetworkPolicy API and list its applicable policy objects. An installed API with no active policy objects is not itself a blocker; any active object or denied/incomplete listing blocks the run.
- The policy-enabled sandbox is the candidate namespace. Remove witness resources before starting the candidate. Never create candidate workloads in the source namespace.
- Do not copy Secrets, PVCs, service-account tokens, credentials, or external service state. If a candidate depends on namespaced configuration absent from the sandbox, report the verification as inconclusive rather than copying sensitive resources or calling it a candidate failure.
- No egress override, implicit retry with broader access, or candidate delivery from an inconclusive/failed isolation gate. Preserve patch validation and apply guards.
- Cleanup the namespace by exact Fixora ownership labels and UID preconditions, wait for asynchronous namespace deletion, and surface leftovers as cleanup failure. `--keep-shadow` is an explicit operator handoff and must report the retained namespace and policy.
- Keep user-facing output concise: isolation status, important dependency limitations, verification outcome, and cleanup outcome.

## Scope

This phase delivers the per-run namespace boundary, enforcement controls, fail-closed outcomes, exact cleanup, and operator documentation. Mocked dependency fidelity, broader failure-specific evidence, cross-run learning, and UI/evidence-card work remain later phases.
