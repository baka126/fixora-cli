---
layout: default
title: Remediation coverage
description: Reviewed diagnosis, AI proposal, verification, delivery, and rollback coverage.
permalink: /reference/remediation-coverage/
section: reference
---

# Remediation coverage

**Upcoming — reviewed 2026-10-04 against checkpoint `fffb721`.** This is a capability inventory, not a promise to repair every cluster failure. There are **34 registered analyzer definitions, 24 precision checks, and 109 named source/status entries**. The earlier audit's count of 36 registrations was incorrect. Dynamic API reasons and status summaries are listed separately.

Without AI, Fixora supplies the observed cause/context and deterministic advice; some checks identify configuration risks rather than a proven incident root cause. With AI, it can explain evidence and uncertainty and propose a workload-specific patch. A model narrative or confidence score alone cannot authorize a repair.

The versioned [machine-readable inventory](https://github.com/baka126/fixora-cli/blob/codex/fix-crashloop-guidance/test/coverage/remediation.json) records each named finding's outcome profile. `go test ./test/coverage` checks registry entries, precision checks, and known static status emission sites against it, and tests that a status alone cannot enable apply. Dynamic reasons need manual classification; static extraction is not a complete analysis of all possible runtime strings.

## Outcome profiles

An AI proposal is possible for every failing profile when enough evidence is available. Automatic candidate selection depends on the **actual changed fields**, not the analyzer name or original status. Non-template proposals, mixed change families, arbitrary startup commands, scheduling/security-policy changes, and unsupported fields remain review-only or are rejected.

| Profile | Without AI / prerequisites | AI validation | Shadow | Delivery |
| --- | --- | --- | --- | --- |
| image | Verified ranked digest/platform evidence → image candidate | Image patch; architecture/repository check | Yes, if source is cloneable | Conditional cluster or raw/Kustomize source delivery |
| resources | OOM usage/declared demand or proven request > allocatable → sized candidate | Resource-only patch | Yes, if source is cloneable | Conditional; capacity/usage assumptions still need review |
| config | Unique existing ConfigMap/key for a broken reference → env candidate; missing Secret → operator action | Env references; AI checks referenced ConfigMap keys | ConfigMap path only; Secret-bearing sources blocked | Conditional ConfigMap patch; no invented values/Secrets |
| probe | Unambiguous numeric readiness port mismatch → port candidate | Readiness/liveness/startup probe fields | Yes, if source is cloneable | Conditional; AI may propose path/timing repairs using evidence |
| filesystem | Narrow proved transient-cache write → emptyDir/fsGroup candidate for Deployment/StatefulSet/DaemonSet/ReplicaSet templates | Cache-only filesystem policy; preserve security and storage | Yes, if source is cloneable | Conditional; AI can choose workload-specific mount/name; live evidence is Deployment-only |
| connection | Approved connection ConfigMap + ready TCP Service + proved client consumption → env candidate (Deployment only) | Env patch; source/key/endpoint checks | Yes, if source is cloneable | Conditional; no dependency creation or credential invention |
| startup | CrashLoop is a symptom; command repair requires explicit matching --startup-baseline (Deployment only); other proved causes can use another family | restore-command baseline; arbitrary AI commands stay review-only | Yes for approved baseline | Conditional baseline restore; guessed startup never automatic |
| review | Finding/context + recommended investigation; some planner templates remain placeholders | AI source suggestion possible; no supported automatic family established by this finding alone | No dedicated verifier for this finding | Review source proposal/manual prerequisites; no automatic delivery claim |
| diagnosis | Observation, missing evidence, generic/API status or report envelope | AI can explain or suggest investigation; generic status is not a repair | No verifier from status alone | Collect evidence; no mutation from status alone |
| healthy | Healthy/owned pods healthy → no repair; OK is a report envelope | Healthy targets bypass AI repair selection | Not needed | No repair |

Every conditional delivery path also requires matching resource/container identity, no unresolved placeholders, sufficient confidence, source ownership checks, a supported target and successful verification. Inferred and AI repairs require shadow verification; `--quick` does not waive this. A shadow Pod becoming Ready checks the tested process/probes, not application semantics, production load, an entire controller fleet, storage migration, or dependency authentication.

Shadow cloning rejects sources using Secrets, persistent storage and other restricted settings. A validator accepting a field does not imply the live workload can be cloned. GitOps/Helm ownership blocks direct cluster delivery; automatic Helm values/PR translation is also blocked. Validated raw manifests and Kustomize paths have source delivery support. Rollout and rollback support must be assessed independently of patch validation.

## Registered analyzers

All entries below also have the registered status-summary fallback when enabled and readable. Filters/defaults and skipped checks affect execution. A summary containing an unhealthy marker is weaker evidence than a precision finding; inventory-only objects may produce no finding.

| Analyzer | Detection evidence | Repair outcome |
| --- | --- | --- |
| ConfigMap | ConfigMap inventory/precision | Review/diagnosis; see named findings below |
| CronJob | CronJob precision + owned pods | Common profiles from failing owned/container evidence; controller-only findings require review |
| DaemonSet | DaemonSet precision + owned pods | Common profiles from failing owned/container evidence; controller-only findings require review |
| Deployment | Deployment precision + owned pods | Common profiles from failing owned/container evidence; controller-only findings require review |
| Gateway | Gateway API | Review/diagnosis; see named findings below |
| GatewayClass | Gateway API | Review/diagnosis; see named findings below |
| HPA | HPA target/request precision | Review/diagnosis; see named findings below |
| HTTPRoute | Gateway API | Review/diagnosis; see named findings below |
| Ingress | Ingress backends/TLS | Review/diagnosis; see named findings below |
| Job | Job precision + owned pods | Common profiles from failing owned/container evidence; controller-only findings require review |
| KedaScaledObject | Registered status summary | Review/diagnosis; see named findings below |
| KyvernoPolicyReport | Registered status summary | Review/diagnosis; see named findings below |
| MutatingWebhook | Webhook backend precision | Review/diagnosis; see named findings below |
| NetworkPolicy | Selector/pod inventory | Review/diagnosis; see named findings below |
| Node | Node conditions + pod capacity context | Review/diagnosis; see named findings below |
| OLMCatalogSource | OLM precision | Review/diagnosis; see named findings below |
| OLMClusterCatalog | OLM precision | Review/diagnosis; see named findings below |
| OLMClusterExtension | OLM precision | Review/diagnosis; see named findings below |
| OLMClusterServiceVersion | OLM precision | Review/diagnosis; see named findings below |
| OLMInstallPlan | OLM precision | Review/diagnosis; see named findings below |
| OLMOperatorGroup | OLM precision | Review/diagnosis; see named findings below |
| OLMSubscription | OLM precision | Review/diagnosis; see named findings below |
| PDB | Disruption availability | Review/diagnosis; see named findings below |
| PVC | PVC events/class/volume precision | Review/diagnosis; see named findings below |
| Pod | Container states/events/logs/probes | Common profiles from failing owned/container evidence; controller-only findings require review |
| ReplicaSet | ReplicaSet creation + owned pods | Common profiles from failing owned/container evidence; controller-only findings require review |
| ResourceClaim | DeviceClass reference (opt-in/optional API) | Review/diagnosis; see named findings below |
| Secret | Key/schema/pull references (opt-in --check-secret-keys) | Review/diagnosis; see named findings below |
| Service | EndpointSlice readiness + target ports | Review/diagnosis; see named findings below |
| StatefulSet | Replica/rollout/storage/service + owned pods | Common profiles from failing owned/container evidence; controller-only findings require review |
| StorageClass | Storage inventory/default classes | Review/diagnosis; see named findings below |
| TrivyConfigAuditReport | Registered status summary | Review/diagnosis; see named findings below |
| TrivyVulnerabilityReport | Registered status summary | Review/diagnosis; see named findings below |
| ValidatingWebhook | Webhook backend precision | Review/diagnosis; see named findings below |

Helm is a source/delivery mode, not a registered analyzer. RBAC and pod security are precision checks; their findings also appear below.

## Named findings

Each row maps explicit emitted status names to an outcome profile above. A trailing `/` is a prefix followed by the relevant container/webhook name. These are source/status pairs; names reused by different resources have separate entries. Kubernetes container reasons listed for `analyzer.go` include common runtime values returned unchanged.

| Source in internal/analyzer | Finding status | Outcome profile |
| --- | --- | --- |
| analyzer.go | `ConnectionRefused` | connection |
| analyzer.go | `CrashLoopBackOff` | startup |
| analyzer.go | `CreateContainerConfigError` | config |
| analyzer.go | `CreateContainerError`, `PodFailed`, `RunContainerError` | review |
| analyzer.go | `ErrImagePull`, `ImagePullBackOff` | image |
| analyzer.go | `Healthy`, `OK`, `OwnedPodsHealthy` | healthy |
| analyzer.go | `NoOwnedPods`, `PodsUnreadable`, `ProblemDetected`, `Unknown` | diagnosis |
| analyzer.go | `OOMKilled`, `Pending`, `Unschedulable` | resources |
| analyzer.go | `ProbeFailure` | probe |
| certexpiry.go | `TLSCertExpired`, `TLSCertExpiringSoon` | review |
| configmap.go | `ConfigMapInvalidFormat`, `ConfigMapShared`, `Empty`, `Large`, `Unused` | review |
| configmap.go | `PodListUnavailable` | diagnosis |
| cronjob.go | `CronJobOverlap`, `Suspended` | review |
| daemonset.go | `DaemonSetFleetHeterogeneous`, `DaemonSetUnderScheduled`, `NotReady` | review |
| deployment.go | `ReplicasMismatch` | review |
| gateway_api.go | `BackendPortMismatch`, `ConditionFalse`, `MissingBackendRef`, `MissingParentRef` | review |
| hpa.go | `MissingResourceRequests`, `MissingScaleTarget`, `MissingScaleTargetRef`, `ScaleTargetUnreadable` | review |
| ingress.go | `IngressPortMismatch`, `MissingBackendService`, `MissingIngressClass`, `MissingTLSSecret`, `TLSCertUnreadable`, `TLSSecretUnreadable` | review |
| job.go | `Failed`, `JobRetrying`, `Suspended` | review |
| logsignals.go | `ApplicationPanic`, `AuthenticationFailed`, `ConfigParseError`, `DiskFull`, `DNSResolutionFailed`, `MissingEnvVar`, `TLSHandshakeError` | review |
| logsignals.go | `DatabaseUnreachable`, `DependencyTimeout` | connection |
| logsignals.go | `ExecFormatError` | image |
| logsignals.go | `PermissionDenied` | filesystem |
| networkpolicy.go | `EmptyPodSelector`, `NoSelectedPods` | review |
| networkpolicy.go | `PodListUnavailable` | diagnosis |
| olm.go | `CatalogSourceConnectionUnhealthy`, `ClusterCatalogUnhealthy`, `ClusterExtensionUnhealthy`, `CSVNotSucceeded`, `InstallPlanIncomplete`, `MultipleOperatorGroups`, `SubscriptionNotCurrent` | review |
| pdb.go | `DisruptionsBlocked` | review |
| pod_security.go | `DefaultServiceAccount`, `HostNamespaceEnabled`, `PrivilegedContainer/`, `RunAsNonRootMissing/` | review |
| pod_terminating.go | `PodStuckTerminating` | review |
| pvc.go | `Lost`, `MissingStorageClass`, `Pending`, `ProvisioningFailed`, `SmallStorageRequest`, `StorageClassNotFound`, `VolumeAttachFailed`, `VolumeAwaitingConsumer`, `VolumeResizePending` | review |
| rbac.go | `ClusterAdminBinding`, `WildcardRBAC` | review |
| registry.go | `Unhealthy` | diagnosis |
| replicaset.go | `FailedCreate` | review |
| resourceclaim.go | `DeviceClassNotFound` | review |
| secret.go | `MissingPullSecret`, `SecretInvalidBase64`, `SecretMissingKey` | review |
| service_ports.go | `ServicePortMismatch` | review |
| service.go | `EndpointsBlockedByReadinessGate`, `NoEndpoints`, `NotReadyEndpoints` | review |
| statefulset.go | `HeadlessServiceMissing`, `ReplicasMismatch`, `StatefulSetRolloutBlocked`, `StatefulSetStorageUnbindable` | review |
| storage.go | `Failed`, `MultipleDefaultStorageClasses`, `Released` | review |
| webhook.go | `FailClosedWebhook/`, `HighWebhookTimeout/`, `MissingWebhookService/`, `WebhookServiceUnreadable/` | review |

The `resources` profile for Pending/Unschedulable applies only to proved requests exceeding node allocatable. Taints, affinity, quota, policy and PVC scheduling failures keep review/diagnosis outcomes. CrashLoopBackOff can become OOMKilled, PermissionDenied or another cause after evidence/log refinement. The `startup` profile does not make every CrashLoop a command problem.

ServicePortMismatch, MissingResourceRequests, controller rollout/replica findings and similar detections currently lack dedicated automatic repair inference. In particular, the HPA planner's substring match on “HPA” does **not** select its template for the precision status MissingResourceRequests. This is a gap, not completed HPA remediation. A review-only template can contain placeholders and is not a concrete repair.

## Dynamic findings and optional APIs

| Source | Runtime status form | Outcome |
| --- | --- | --- |
| registry.go | compactMap(object.status) | All registered kinds may emit an API status summary; not a stable enum or proof of root cause. Profile: review. |
| analyzer.go | container waiting/termination reason or pod.status.reason | Unknown Kubernetes/runtime reasons remain evidence collection; recognized image/config/OOM reasons use their named profiles. Controller findings can inherit these statuses from owned pods. Profile: review. |
| node.go | condition.reason or condition.type | NotReady/pressure context; no automatic node operations. Profile: review. |
| gateway_api.go | condition.reason or ConditionFalse | Route/listener/controller conditions; no automated cross-resource verification. Profile: review. |
| storage.go | PersistentVolume phase Released or Failed | Persistent data and reclaim/binding changes require review. Profile: review. |
| webhook.go | named status prefix + webhook name | Documented prefixes ending in / carry the webhook identity. Profile: review. |
| pod_security.go | named status prefix + container name | Documented prefixes ending in / carry container identity. Profile: review. |

KEDA, Kyverno, Trivy, Gateway API and OLM checks require installed, readable APIs. Registered KEDA/Kyverno/Trivy checks inspect status markers; they do not enumerate every vulnerability, failed policy result or scaler failure. ResourceClaim checking is optional. API read failures are skipped checks or diagnostic findings, never proof that an object is absent.

The 24 precision checks are: `service-endpoints`, `service-ports`, `ingress-backends`, `hpa-targets`, `pdb-disruptions`, `webhook-backends`, `gateway-api`, `network-policy`, `rbac-risk`, `pod-security`, `node-conditions`, `pvc`, `storage`, `configmap`, `olm-operators`, `resource-claim`, `deployment-replicas`, `daemonset`, `statefulset`, `job`, `cronjob`, `replicaset`, `secret`, `pod-terminating`.

## Controller verification and rollback limits

| Target | Shadow extraction | After-apply gate | Rollback advice |
| --- | --- | --- | --- |
| Deployment | Pod template | Rollout | rollout undo |
| StatefulSet | Pod template; persistent-storage sources can be blocked | Rollout | rollout undo; data is not rolled back |
| DaemonSet | Pod template; one shadow does not prove every node | Rollout | rollout undo |
| ReplicaSet | Pod template | Unsupported rollout can be skipped/unknown | Restore source/known-good manifest |
| Job | Pod template; batch-specific isolation restrictions apply | Completion condition | Restore source; no rollout undo |
| CronJob | Job pod template; batch-specific isolation restrictions apply | Schedule/suspension/recent-run checks; next scheduled run is not awaited | Restore source; no rollout undo |
| Standalone Pod | Pod spec | Unsupported rollout can be skipped/unknown; immutable fields can block apply | Restore known-good manifest |
| Service, Ingress, HPA, PDB, PVC, Node, policies, operators | No dedicated shadow for these findings | No complete specialized delivery gate | Reviewed source/manual action |
| Helm/GitOps source | Direct cluster delivery blocked | Render/review source; automatic Helm translation blocked | Revert source; Helm rollback advice may exist |

These describe code paths, not equal live coverage across controllers. The existing eight-workload live baseline used Deployments. Expanding controller parity is point 2 and remains outstanding.

## Regression baseline

The new provider-independent regression suite drives the real inference registry → planner → shadow patch policy for six candidate-producing scenarios, checks exact changed fields, and confirms recovered workloads are not repaired. It intentionally does not duplicate AWS live tests or call a provider.

| Demo / variant | Expected protected outcome | Regression evidence |
| --- | --- | --- |
| imagepull-demo | Ranked digest-pinned image; correct container | TestDemoRepairRegression/imagepull; existing image/platform tests |
| pending-demo | Capacity-derived resource candidate only with allocatable proof | TestDemoRepairRegression/pending; resource inference refusal tests |
| oomkilled-demo | Declared 100Mi demand → request 112Mi / limit 160Mi | TestDemoRepairRegression/oomkilled; OOM classification/demand ambiguity tests |
| probe-demo | Observed port mismatch → readiness port 8080 | TestDemoRepairRegression/probe; analyzer probe-window/attribution tests |
| security-demo | Proved transient /var/cache → emptyDir/fsGroup 1000; existing security preserved | TestDemoRepairRegression/security; AI storage/security and filesystem validation tests |
| dependency-demo | Approved connection ConfigMap + ready endpoint → DB_HOST/DB_PORT references | TestDemoRepairRegression/dependency; wrong-owner/endpoint/config refusal tests |
| missing-config-demo (Secret) | Refuse invention; operator supplies Secret, workload recovers naturally | TestDemoMissingSecretRequiresOperatorValue; TestDemoSecretConsumerRecovery |
| crashloop-demo | No guessed startup; matching operator baseline only | TestDemoUnknownStartupHasNoInventedRepair; TestDemoStartupBaselineRegression |
| ConfigMap typo variant | Unique existing named/keyed ConfigMap only | Existing TestEnvInferrerCorrectsTypoedConfigMap and ambiguity/key tests |
| AI custom probe/filesystem variants | Field-based dynamic candidate, live identity checks and mandatory shadow | Existing ai_repair_test.go and filesystem/startup validation tests |

Run the baseline without AWS or provider credentials:

```sh
go test ./test/coverage ./internal/infer ./internal/cli ./internal/analyzer ./internal/shadow ./internal/kube ./internal/fix
go test ./...
```

The AI delivery E2E suite requires a cluster and real provider. It is not executed by the ordinary Go unit suite. Its missing-config fixture deliberately uses a ConfigMap typo and correctly expects delivery; the EKS missing-config demo references an absent Secret and needs operator action. The unit regression separately replays the Secret consumer becoming Ready after its prerequisite is supplied; it does not create a live Secret or establish live delivery success.

Validation on 2026-10-04: the full Go unit suite passed with a fresh run; the new regression/inventory tests passed under the race detector; vet passed for the changed test packages; the E2E delivery suite compiled with both build tags. Live E2E execution was unavailable because Docker is absent. Eight temporary Go build overlays proved the tests detect removal of each of the six working inferrers, the Secret refusal guard and the startup baseline's mandatory shadow guard. The overlays did not edit production files. No EKS workloads or AI provider calls were used for this checkpoint.

## Next work

1. **Completed inventory/regression protection:** this reference, the reviewed machine inventory and provider-independent scenarios. No repair-policy expansion.
2. **Controller parity:** exercise common repairs, immutable fields, batch completion, owning-template resolution and rollback across supported targets.
3. **New verification policies:** independently add trusted startup, scheduling, security and coordinated repairs with causal proof.
4. **Helm source delivery:** map changes to chart values/templates, render and compare, then validate.
5. **Cross-resource/stateful coverage:** dependency wiring, scaling/disruption, storage, networking, nodes/admission/operators; retain review gates until dedicated verification exists.

Keep the working Deployment fixtures as the baseline when implementing later points. A new “supported” finding requires positive repair tests, ambiguous/unsafe refusal tests, an explicit verification method and delivery/rollback evidence.
