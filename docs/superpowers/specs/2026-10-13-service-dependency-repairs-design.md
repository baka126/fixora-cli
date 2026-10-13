# Step 5a: verified Service dependency wiring

Status: proposed for review; no implementation approved or performed.
Starting checkpoint: Step 4 `34c30c5`, branch `codex/helm-source-delivery`.

## Intent and decomposition

The user wants dynamic repairs across Kubernetes resources, deterministic root-cause explanations without AI, richer workload-specific proposals with AI, and preservation of working repairs. Step 5 in the saved remediation roadmap spans several independent verification problems. Start with Service dependency wiring, then scope scaling/disruption, storage, and node/admission/operator coverage separately. A Service verifier must not authorize unrelated resource mutations.

Success for this slice: an approved selector or target-port correction can be prepared, independently tested, applied to an unmanaged Service when explicitly requested, and checked or rolled back using exact identity and field guards. A model suggestion, unique-looking application name or declared container port does not establish the intended destination.

## Current evidence

`internal/analyzer/service.go` emits NoEndpoints, NotReadyEndpoints and readiness-gate-specific findings. It prefers EndpointSlice reads and uses legacy Endpoints only when slices cannot be read. `service_ports.go` compares declared selected regular-container ports against Service targetPort. Those declarations describe configuration; they do not prove that an application is listening or that this is the intended backend.

`internal/fix/planner.go` keeps Service selector proposals review-only. The existing shadow and repairpolicy paths prove Pod-template changes and must not be made permissive for Services. Existing AI source proposals and Helm/raw/Kustomize ownership restrictions must remain effective.

## Chosen approach and alternatives

Add a dedicated Service repair policy and verifier beside the existing workload verifier. Reuse cluster readers, redaction, provider contracts, source ownership checks, concise output and field-guarded operation patterns.

An alternative is to promote a unique nearby Ready workload or exposed port automatically. Reject this: uniqueness is not application intent. Another alternative is to apply directly and observe endpoints afterward. Reject this as the initial verifier: it changes production traffic before validating the proposed destination.

Use an approved local policy containing exact Service identity, intended backend workload identity and selected Service port/target port. AI may suggest this information for review, but cannot create authority from its response. An operator-approved policy supplies intent without demo-specific names, commands or port mappings.

## Initial scope

- Standard, selector-managed, non-headless ClusterIP Services and TCP ports only.
- Selector replacement and targetPort correction, individually or together, for an explicitly selected port; every other Service field must remain unchanged.
- Same-namespace backends owned by an explicitly identified Deployment, StatefulSet or DaemonSet. Pod membership and owning-controller identity are checked rather than inferred from name similarity.
- Existing backend workloads only: no dependency/database creation, Secret creation, endpoint authoring, label edits, workload recreation or persistent-data changes.
- No ExternalName, selectorless/headless, NodePort/LoadBalancer, UDP/SCTP, manually managed slices, service-mesh-specific or operator-managed routing automation in this slice. Unsupported cases retain diagnosis and reviewed proposals.
- Helm/GitOps direct delivery stays blocked. Service Helm values translation remains review-only; Step 4's workload-only effect certificate is not broadened implicitly.

## Intent and candidate preparation

Introduce a strict local Service repair policy input, separately dispatched from workload baselines. The implementation plan must choose the final CLI spelling while preserving existing flags and loaders. The document names the Service namespace/name, intended backend kind/name, selected Service port identity, approved selector and targetPort, and a verification contract. Bind the document bytes and live UIDs in memory; reject unknown fields, duplicate YAML keys, paths that escape the declared input and mismatched identities.

Verification contracts support TCP connection checks or HTTP GET against an operator-approved health path and expected status. TCP proves transport reachability only; HTTP status does not prove all application semantics. No supplied credentials, arbitrary shell scripts or model-selected probe commands.

The probe runs a fixed Fixora verifier binary in a digest-pinned image supplied through operator-controlled configuration. The model cannot change the image or verifier. Building/publishing that image is a separate deployment prerequisite; tests can build it locally without publishing. Missing trusted probe configuration produces an explicit prerequisite, not a guessed image.

Collect the live Service, selected workload, current Pods, EndpointSlices and relevant readiness/events. Preserve RBAC/read failures as unavailable evidence. Exclude terminating, non-Ready or replaced Pods. Candidate selection must resolve only to Pods of the approved backend; reject a selector matching unrelated workloads, incomplete owner chains, unsupported ownership or inconsistent per-Pod named-port resolution.

Use the existing provider patch field for AI proposals. Add a Service-specific field classifier/validator; do not widen the workload shadow allowlist or accept new provider-supplied authority. Preserve genuine diagnosis, confidence, recovery and policy blockers. No AI mode explains the observed mismatch and policy-backed candidate; AI mode can add uncertainty and propose a candidate within the same policy.

## Dedicated verification and authority

1. Prepare the exact candidate Service and reject changes outside the approved selector/targetPort fields. Preserve service IPs, address families, public port/protocol, traffic policies and unrelated metadata.
2. Perform server dry-run and compare admitted candidate fields and identity; an admission rewrite or opaque failure refuses verification.
3. With explicit verification consent, create a uniquely named temporary ClusterIP Service in the backend namespace. It uses the candidate selector and port wiring, not the production name. A separate namespace cannot select these Pods.
4. Create a bounded verifier Pod with no service-account token, Secrets, persistent volumes, privilege or host access. Apply ingress/egress isolation using existing policy patterns; restrict test destinations and avoid DNS by using the assigned Service IP. Namespace admission/isolation failures are blockers, not bypasses.
5. Read only controller-owned EndpointSlices for the temporary Service UID. Require exact namespace/service ownership, expected Pod target UIDs, Ready/non-terminating conditions and port resolution. Refuse unexpected/extra endpoints and unreadable or incomplete evidence.
6. Verify each expected Ready backend address and the temporary Service using the approved contract. A single load-balanced success is insufficient to certify all discovered endpoints. Runtime output must be bounded and tied to this verification attempt.
7. Recheck original Service configuration/UID, backend owner UID, Pod membership/configuration and admitted candidate before issuing an opaque in-memory receipt. Cleanup only owned temporary resources by UID on success, failure or cancellation.

Preview/structured diagnostic output never creates resources. Failed verification produces no receipt. JSON flags, --quick, --force-risky, AI confidence and a saved successful report cannot recreate authority. A changed backend set requires preparing and verifying again.

## Delivery and rollback

For explicitly requested direct delivery of an unmanaged Service, use an atomic UID/resourceVersion-tested field patch. Replace the selector exactly; do not accidentally retain obsolete selector keys through partial strategic merge. Change only the selected port's targetPort, preserving all other port entries. Recheck the complete candidate and membership bindings immediately before mutation.

After apply, verify production EndpointSlices against the expected owned backends and repeat the approved connectivity contract. Report endpoint convergence, transport/HTTP verification and application/consumer health as separate outcomes; do not claim a dependent workload recovered unless its own evidence demonstrates it.

Prepare an inverse of only the edited fields, preserving absent/null semantics. Rollback requires the same Service UID and the current edited fields to still equal Fixora's candidate. Refuse rollback if another actor changed them. Keep explicit rollback confirmation; do not overwrite unrelated concurrent changes.

Raw/Kustomize source delivery may use the existing reviewed patch/PR workflow with truthful source versus runtime claims and exact file scope. It must not weaken existing source validation or claim production reconciliation. Helm source suggestions remain manual for Service candidates until a separate Service render certificate is designed.

## Output and acceptance

Default text: Service identity, observed cause, proposed selector/port change, validation status, verification scope and next action. Detailed Pod/slice evidence belongs behind --proof. Report unresolved intent as a missing prerequisite, not a generic placeholder patch. Never label unverified or merely source-written changes healthy.

Positive tests: numeric and named targetPort correction; exact selector replacement including obsolete key removal; independently generated workload/container/label names; multiple Ready backends; unchanged Service IP/other ports; TCP versus HTTP claim scope; preview no side effects; successful guarded apply and field inverse rollback.

Refusals: ambiguous backend/ports, unrelated Pods, mismatched owners/UIDs/namespaces, non-Ready/terminating or replaced Pods, absent/list-denied slices, stale/manual/unexpected endpoints, admission rewrites, unsupported Service types/protocols, source ownership, missing intent/probe prerequisite, failed backend or load-balanced checks, stale/replayed receipt, cancellation and conflicting rollback fields. Temporary-resource cleanup and partial-completion reporting require dedicated tests.

Regression: all Step 1–4 provider, workload, controller, coordinated-policy and Helm source tests remain green. Use fake Kubernetes/transport seams plus disposable local-cluster tests where available. Live EKS tests require explicit test deployment intent and a separate synthetic namespace; never reset the existing fixora-demo resources as an incidental part of development. Preserve local Terraform/state/demo files and push only product code/docs/tests.

This spec defines no storage/scaling/node/operator verifier and makes no promise that all cross-resource failures become repairable.

## References

- Kubernetes Service: https://kubernetes.io/docs/concepts/services-networking/service/
- Kubernetes EndpointSlices: https://kubernetes.io/docs/concepts/services-networking/endpoint-slices/
- Existing coverage roadmap: docs/reference/remediation-coverage.md
- Working Step 4 checkpoint: docs/superpowers/reports/2026-10-12-helm-source-delivery.md
