package analyzer

import (
	"context"
	"strings"
	"testing"

	"github.com/fixora/kubectl-fixora/internal/kube"
)

func TestConfigMapAnalyzerImportsK8sGPTUsageChecks(t *testing.T) {
	ctx := scanContextWithItems(map[string][]map[string]any{
		"configmaps": {
			configMapFixture("prod", "unused", map[string]any{"key": "value"}, nil, nil, nil),
			configMapFixture("prod", "empty", nil, nil, nil, nil),
			configMapFixture("prod", "large", map[string]any{"payload": strings.Repeat("x", 1024*1024+1)}, nil, nil, nil),
			configMapFixture("prod", "used", map[string]any{"key": "value"}, nil, nil, nil),
			configMapFixture("prod", "dashboard", map[string]any{"dashboard.json": "{}"}, nil, map[string]any{"grafana_dashboard": "1"}, nil),
			configMapFixture("prod", "skip", map[string]any{"key": "value"}, nil, nil, map[string]any{"k8sgpt.ai/skip-usage-check": "true"}),
		},
		"pods": {
			{
				"metadata": map[string]any{"namespace": "prod", "name": "api"},
				"spec": map[string]any{
					"containers": []any{map[string]any{
						"name": "api",
						"envFrom": []any{map[string]any{
							"configMapRef": map[string]any{"name": "used"},
						}},
					}},
				},
			},
		},
	})
	findings, err := New(fakeReader{}, Options{Namespace: "prod"}).analyzeConfigMaps(ctx)
	if err != nil {
		t.Fatal(err)
	}
	assertFindingStatus(t, findings, "Unused")
	assertFindingStatus(t, findings, "Empty")
	assertFindingStatus(t, findings, "Large")
	assertNoFindingForResource(t, findings, "used")
	assertNoFindingForResource(t, findings, "dashboard")
	assertNoFindingForResource(t, findings, "skip")
}

func TestNetworkPolicyAnalyzerImportsK8sGPTSelectorChecks(t *testing.T) {
	ctx := scanContextWithItems(map[string][]map[string]any{
		"networkpolicies": {
			{
				"metadata": map[string]any{"namespace": "prod", "name": "all-pods"},
				"spec":     map[string]any{"podSelector": map[string]any{"matchLabels": map[string]any{}}},
			},
			{
				"metadata": map[string]any{"namespace": "prod", "name": "no-match"},
				"spec":     map[string]any{"podSelector": map[string]any{"matchLabels": map[string]any{"app": "missing"}}},
			},
			{
				"metadata": map[string]any{"namespace": "prod", "name": "matched"},
				"spec":     map[string]any{"podSelector": map[string]any{"matchLabels": map[string]any{"app": "api"}}},
			},
		},
		"pods": {
			{"metadata": map[string]any{"namespace": "prod", "name": "api", "labels": map[string]any{"app": "api"}}},
			{"metadata": map[string]any{"namespace": "other", "name": "missing", "labels": map[string]any{"app": "missing"}}},
		},
	})
	findings, err := New(fakeReader{}, Options{Namespace: "prod"}).analyzeNetworkPolicies(ctx)
	if err != nil {
		t.Fatal(err)
	}
	assertFindingStatus(t, findings, "EmptyPodSelector")
	assertFindingStatus(t, findings, "NoSelectedPods")
	assertNoFindingForResource(t, findings, "matched")
}

func TestServiceAnalyzerUsesEndpointSlices(t *testing.T) {
	ctx := scanContextWithItems(map[string][]map[string]any{
		"services": {
			{
				"metadata": map[string]any{"namespace": "prod", "name": "api"},
				"spec":     map[string]any{"selector": map[string]any{"app": "api"}},
			},
		},
		"endpointslices.discovery.k8s.io": {
			{
				"metadata": map[string]any{"namespace": "prod", "name": "api-abc", "labels": map[string]any{"kubernetes.io/service-name": "api"}},
				"endpoints": []any{
					map[string]any{"addresses": []any{"10.0.0.10"}, "conditions": map[string]any{"ready": false}},
				},
			},
		},
	})
	findings, err := New(fakeReader{}, Options{Namespace: "prod"}).analyzeServiceEndpoints(ctx)
	if err != nil {
		t.Fatal(err)
	}
	assertFindingStatus(t, findings, "NoEndpoints")
	assertFindingStatus(t, findings, "NotReadyEndpoints")
}

func TestNodeAnalyzerImportsK8sGPTConditionChecks(t *testing.T) {
	ctx := scanContextWithItems(map[string][]map[string]any{
		"nodes": {
			{
				"metadata": map[string]any{"name": "worker-1"},
				"status": map[string]any{"conditions": []any{
					map[string]any{"type": "Ready", "status": "False", "reason": "KubeletNotReady", "message": "runtime down"},
					map[string]any{"type": "MemoryPressure", "status": "True", "reason": "KubeletHasInsufficientMemory"},
					map[string]any{"type": "EtcdIsVoter", "status": "True"},
				}},
			},
		},
	})
	findings, err := New(fakeReader{}, Options{}).analyzeNodes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	assertFindingStatus(t, findings, "KubeletNotReady")
	assertFindingStatus(t, findings, "KubeletHasInsufficientMemory")
	if len(findings) != 2 {
		t.Fatalf("expected two node findings, got %#v", findings)
	}
}

func TestPVCAnalyzerImportsK8sGPTProvisioningAndStorageChecks(t *testing.T) {
	ctx := NewScanContext(context.Background(), fakeReader{
		items: map[string][]map[string]any{
			"pvc": {
				pvcFixture("prod", "pending", "Pending", nil),
				pvcFixture("prod", "lost", "Lost", nil),
				pvcFixture("prod", "small", "Bound", map[string]any{"resources": map[string]any{"requests": map[string]any{"storage": "512Mi"}}}),
				pvcFixture("prod", "no-class", "Bound", map[string]any{"resources": map[string]any{"requests": map[string]any{"storage": "2Gi"}}}),
				pvcFixture("prod", "healthy", "Bound", map[string]any{"storageClassName": "fast", "resources": map[string]any{"requests": map[string]any{"storage": "2Gi"}}}),
			},
			"storageclasses": {
				{"metadata": map[string]any{"name": "fast"}},
			},
		},
		events: []kube.Event{{
			Metadata:       kube.ObjectMeta{Namespace: "prod"},
			InvolvedObject: kube.ObjectReference{Namespace: "prod", Name: "pending"},
			Reason:         "ProvisioningFailed",
			Message:        "failed to provision volume with StorageClass slow",
			LastTime:       "2026-06-06T10:00:00Z",
		}},
	}, Options{Namespace: "prod"})
	findings, err := New(fakeReader{}, Options{Namespace: "prod"}).analyzePVCs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	assertFindingStatus(t, findings, "ProvisioningFailed")
	assertFindingStatus(t, findings, "Lost")
	assertFindingStatus(t, findings, "SmallStorageRequest")
	assertFindingStatus(t, findings, "MissingStorageClass")
	assertNoFindingForResource(t, findings, "healthy")
}

func TestGatewayAPIAnalyzerImportsK8sGPTRouteDependencyChecks(t *testing.T) {
	ctx := scanContextWithItems(map[string][]map[string]any{
		"gatewayclasses.gateway.networking.k8s.io": {
			{
				"kind":     "GatewayClass",
				"metadata": map[string]any{"name": "edge"},
				"status": map[string]any{"conditions": []any{
					map[string]any{"type": "Accepted", "status": "False", "reason": "InvalidParameters", "message": "bad parameters"},
				}},
			},
		},
		"gateways.gateway.networking.k8s.io": {
			{"kind": "Gateway", "metadata": map[string]any{"namespace": "prod", "name": "edge"}},
		},
		"services": {
			{"metadata": map[string]any{"namespace": "prod", "name": "api"}, "spec": map[string]any{"ports": []any{map[string]any{"port": 80}}}},
		},
		"httproutes.gateway.networking.k8s.io": {
			{
				"kind":     "HTTPRoute",
				"metadata": map[string]any{"namespace": "prod", "name": "bad-route"},
				"spec": map[string]any{
					"parentRefs": []any{map[string]any{"name": "missing-gateway"}},
					"rules": []any{map[string]any{"backendRefs": []any{
						map[string]any{"name": "missing-service", "port": 8080},
						map[string]any{"name": "api", "port": 8080},
					}}},
				},
			},
		},
	})
	findings, err := New(fakeReader{}, Options{Namespace: "prod"}).analyzeGatewayAPI(ctx)
	if err != nil {
		t.Fatal(err)
	}
	assertFindingStatus(t, findings, "InvalidParameters")
	assertFindingStatus(t, findings, "MissingParentRef")
	assertFindingStatus(t, findings, "MissingBackendRef")
	assertFindingStatus(t, findings, "BackendPortMismatch")
}

func TestOLMAnalyzerImportsK8sGPTOperatorChecks(t *testing.T) {
	ctx := scanContextWithItems(map[string][]map[string]any{
		"catalogsources.operators.coreos.com": {
			{"metadata": map[string]any{"namespace": "openshift-marketplace", "name": "broken"}, "status": map[string]any{"connectionState": map[string]any{"lastObservedState": "TRANSIENT_FAILURE", "address": "catalog:50051"}}},
		},
		"subscriptions.operators.coreos.com": {
			{"metadata": map[string]any{"namespace": "prod", "name": "sub"}, "status": map[string]any{"state": "UpgradePending", "conditions": []any{map[string]any{"status": "False", "reason": "ResolutionFailed", "message": "no bundle"}}}},
		},
		"installplans.operators.coreos.com": {
			{"metadata": map[string]any{"namespace": "prod", "name": "ip"}, "status": map[string]any{"phase": "Installing"}},
		},
		"clusterserviceversions.operators.coreos.com": {
			{"metadata": map[string]any{"namespace": "prod", "name": "csv"}, "status": map[string]any{"phase": "Pending"}},
		},
		"operatorgroups.operators.coreos.com": {
			{"metadata": map[string]any{"namespace": "prod", "name": "og-a"}},
			{"metadata": map[string]any{"namespace": "prod", "name": "og-b"}},
		},
		"clustercatalogs.olm.operatorframework.io": {
			{"metadata": map[string]any{"name": "bad-catalog"}, "spec": map[string]any{"source": map[string]any{"image": map[string]any{"ref": "not a ref"}}}, "status": map[string]any{"conditions": []any{map[string]any{"type": "Serving", "status": "False", "reason": "Unpacked", "message": "cannot serve"}}}},
		},
		"clusterextensions.olm.operatorframework.io": {
			{"metadata": map[string]any{"name": "bad-extension"}, "spec": map[string]any{"source": map[string]any{"sourceType": "Bundle", "catalog": map[string]any{"upgradeConstraintPolicy": "Unsafe"}}}, "status": map[string]any{"conditions": []any{map[string]any{"type": "Installed", "status": "False", "reason": "InstallFailed"}}}},
		},
	})
	findings, err := New(fakeReader{}, Options{Namespace: "prod"}).analyzeOLM(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, status := range []string{"CatalogSourceConnectionUnhealthy", "SubscriptionNotCurrent", "InstallPlanIncomplete", "CSVNotSucceeded", "MultipleOperatorGroups", "ClusterCatalogUnhealthy", "ClusterExtensionUnhealthy"} {
		assertFindingStatus(t, findings, status)
	}
}

func scanContextWithItems(items map[string][]map[string]any) *ScanContext {
	return NewScanContext(context.Background(), fakeReader{items: items}, Options{Namespace: "prod"})
}

func configMapFixture(namespace, name string, data, binaryData, labels, annotations map[string]any) map[string]any {
	return map[string]any{
		"metadata":   map[string]any{"namespace": namespace, "name": name, "labels": labels, "annotations": annotations},
		"data":       data,
		"binaryData": binaryData,
	}
}

func pvcFixture(namespace, name, phase string, spec map[string]any) map[string]any {
	if spec == nil {
		spec = map[string]any{}
	}
	return map[string]any{
		"metadata": map[string]any{"namespace": namespace, "name": name},
		"spec":     spec,
		"status":   map[string]any{"phase": phase},
	}
}

func assertFindingStatus(t *testing.T, findings []Finding, status string) {
	t.Helper()
	for _, finding := range findings {
		if finding.Status == status {
			return
		}
	}
	t.Fatalf("expected finding status %q, got %#v", status, findings)
}

func assertNoFindingForResource(t *testing.T, findings []Finding, resource string) {
	t.Helper()
	for _, finding := range findings {
		if finding.ResourceName == resource {
			t.Fatalf("did not expect finding for %q, got %#v", resource, finding)
		}
	}
}
