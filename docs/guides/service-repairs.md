---
layout: default
title: Service repairs
description: Independently approved Service routing intent, verification, delivery and rollback.
permalink: /guides/service-repairs/
section: guides
---

# Service repairs

Fixora diagnoses Service failures without AI. With AI it can explain the evidence and propose a selector/targetPort change. Automated routing repair requires independent local intent; a model cannot choose a backend, authorize a destination, supply verifier commands or invent a dependency.

Supported targets are selector-managed, non-headless ClusterIP Services with TCP ports. Backends must be Ready Pods controlled by the exact same-namespace Deployment, StatefulSet or DaemonSet named in the policy. Every selected Pod must belong to that backend. ExternalName, headless, selectorless, NodePort/LoadBalancer, UDP/SCTP, manually managed endpoints, mesh/operator routing and dependency/credential creation remain review work.

Create a local policy describing your intended routing and health contract:

```yaml
apiVersion: fixora.io/v1alpha1
kind: ServiceRepairPolicy
metadata:
  name: approved-api-route
  namespace: application
spec:
  service: {name: api-entry}
  backend: {kind: Deployment, name: api-workers}
  selector: {component: api}
  port: {name: public, port: 80, targetPort: app-http}
  verification:
    method: http
    path: /ready
    expectedStatus: 200
    timeout: 5s
    probeImage: your-registry/fixora-service-probe@sha256:REPLACE_WITH_APPROVED_IMAGE_DIGEST
```

Use a real 64-character SHA-256 image digest. `targetPort` is a numeric port or declared regular-container port name. HTTP permits a relative GET path and exact 200–399 response status; no queries, authentication, redirects, DNS or proxy forwarding. For TCP use `method: tcp` and omit `path`/`expectedStatus`. Per-check timeout is 1–10 seconds, default 5 seconds.

```sh
kubectl fixora fix Service/api-entry -n application --no-ai \
  --service-policy ./api-route.yaml --preview

kubectl fixora fix Service/api-entry -n application --ai \
  --service-policy ./api-route.yaml --delivery cluster

kubectl fixora fix Service/api-entry -n application --no-ai \
  --service-policy ./api-route.yaml --delivery patch --yes --out ./routing-patch.yaml
```

Preview and JSON/YAML output are read-only even with `--yes` or `--apply`. Approved backend/selector/port intent is included as proposal context for AI; the verifier image and commands are kept out of this context. AI patch fields must exactly match the independent policy change, with sufficient confidence; unsupported/mixed proposals are refused for review. `--quick`, `--force-risky` and `--keep-shadow` cannot bypass verification or retain verifier resources. Workload policy/baseline and editable-patch flags cannot override Service intent.

Verification performs server dry-run, creates a temporary Service with the approved routing, and waits for exact controller-owned EndpointSlices. A restricted, fixed verifier checks every captured backend address and every Service IP. It has no service-account token, Secrets, PVCs, host access or shell commands. A NetworkPolicy restricts configured ingress/egress, but this does not prove the cluster CNI's enforcement. Each created resource is tracked by its actual UID, and deletion completion is checked. Cleanup failures block delivery and report owned leftovers.

The overall session defaults to three minutes. An explicit `--shadow-timeout` controls the Service session and Pod active deadline. Membership and routing are checked again before certification and production apply; stale evidence requires a new plan. Limits are 64 backend Pods, 128 backend addresses, 130 total probe targets and 64 KiB request/report size.

Direct delivery tests the original Service UID/resourceVersion atomically and changes only the full selector and chosen port's targetPort. Production verification probes the real Service using a fresh verifier session. Passing proves the selected TCP/HTTP contract; it does not prove client NetworkPolicy permissions, dependency authentication, business behavior or consumer recovery.

After a failed production gate, interactive execution can explicitly restore only the edited fields. Concurrent edits to those fields or a replacement Service block rollback; unrelated metadata/ports are preserved. `--yes` does not authorize rollback. A restored original configuration can still be broken. Transport failures after patch submission are reported as uncertain, with no automatic retry.

Helm/GitOps ownership blocks direct cluster delivery. `--delivery pr --repo <path> --yes` retains existing raw/Kustomize reviewed-source and exact-file Git checks. Raw output is a review block to merge into the source manifest; Kustomize output is a strategic merge patch. Source render validation/reconciliation is distinct from the temporary routing contract. Helm Service values mapping is manual review, with no automatic values write or PR claim.

## Build and approve the verifier

The fixed binary lives in `cmd/fixora-service-probe`; the recipe is `build/service-probe.Dockerfile`. Build for your cluster architecture, inspect the image, and publish through your normal approved registry workflow. Fixora does not build or publish an image automatically.

```sh
docker build --build-arg TARGETARCH=amd64 \
  -f build/service-probe.Dockerfile -t your-registry/fixora-service-probe:reviewed .
# Publish through your approved registry workflow, then use its manifest digest.
```

The CLI needs typed Kubernetes API access, dry-run/patch permission for the target Service, reads of namespace/backend/Pods/EndpointSlices, and create/read/delete permission for temporary Services, verifier Pods, Pod logs and NetworkPolicies. Admission settings, unavailable APIs, image pull failures and failed connectivity remain explicit prerequisites or verification failures.

## Local live fixtures

`hack/e2e-service-probe.sh` explicitly creates a dedicated two-node local kind cluster and loopback registry, builds the fixed image, resolves its manifest digest, and runs the tagged Service E2E tests. It deletes only its dedicated test cluster/registry on exit and uses a separate kubeconfig. Docker, kind, kubectl, rg and Go are required. It refuses existing resources with its dedicated names.

Tests cover numeric/named targetPort with arbitrary Deployment/StatefulSet/DaemonSet names, production contracts, field-only rollback, mixed owners and unready backends. Unit/client-go fake tests do not establish real Kubernetes admission, network or CNI behavior. Live fixtures must be executed separately; compilation alone is not a live pass.
