package cli

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/fixora/kubectl-fixora/internal/analyzer"
	"github.com/fixora/kubectl-fixora/internal/kube"
)

type askInventoryReader struct {
	askScanReader
	items map[string][]map[string]any
	calls []string
}

func (r *askInventoryReader) GetResourceItems(_ context.Context, namespace string, allNS bool, resource string) ([]map[string]any, error) {
	r.calls = append(r.calls, resource+"|"+namespace+"|"+boolString(allNS))
	return r.items[resource], nil
}

func TestAskInventoryUsesNamespaceAndLabelScopeAndAvoidsSecretReads(t *testing.T) {
	reader := &askInventoryReader{items: map[string][]map[string]any{
		"services": {
			askInventoryObject("Service", "api", "payments", "svc-1", map[string]any{"team": "payments"}),
			askInventoryObject("Service", "other", "default", "svc-2", map[string]any{"team": "payments"}),
			askInventoryObject("Service", "unmatched", "payments", "svc-3", map[string]any{"team": "platform"}),
		},
		"nodes":   {askInventoryObject("Node", "node-a", "", "node-1", map[string]any{"team": "payments"})},
		"secrets": {askInventoryObject("Secret", "credentials", "payments", "sec-1", nil)},
	}}
	findings, skipped := askInventoryFindings(context.Background(), reader, options{namespace: "payments", labelSelector: "team=payments"}, analyzer.AllDiagnosticFilters())
	if len(skipped) != 0 {
		t.Fatalf("unexpected skips: %#v", skipped)
	}
	identities := make([]string, 0, len(findings))
	for _, finding := range findings {
		identities = append(identities, finding.Namespace+"/"+finding.ResourceKind+"/"+finding.ResourceName)
	}
	if !reflect.DeepEqual(identities, []string{"cluster/Node/node-a", "payments/Service/api"}) {
		t.Fatalf("inventory identities=%#v", identities)
	}
	for _, call := range reader.calls {
		if call == "secrets|payments|false" {
			t.Fatal("Ask inventory must not read Secret objects by default")
		}
	}
}

func TestAskInventoryRestrictsMetadataReadsToSelectedAnalyzers(t *testing.T) {
	reader := &askInventoryReader{items: map[string][]map[string]any{}}
	_, skipped := askInventoryFindings(context.Background(), reader, options{namespace: "payments", quick: true}, []string{"pod"})
	if len(skipped) != 0 {
		t.Fatalf("unexpected skips: %#v", skipped)
	}
	if !reflect.DeepEqual(reader.calls, []string{"pods|payments|false"}) {
		t.Fatalf("quick inventory expanded beyond Pod: %#v", reader.calls)
	}
}

func TestAskInventoryAddsBoundedSafeResourceContext(t *testing.T) {
	deployment := askInventoryObject("Deployment", "api", "payments", "deploy-1", nil)
	deployment["spec"] = map[string]any{
		"replicas": int64(2),
		"template": map[string]any{"spec": map[string]any{"containers": []any{
			map[string]any{"name": "api", "image": "example/api:v1", "command": []any{"hidden-command"}, "ports": []any{map[string]any{"name": "http", "containerPort": int64(8080), "protocol": "TCP"}}, "readinessProbe": map[string]any{"httpGet": map[string]any{"path": "/ready", "port": int64(8080)}}, "env": []any{map[string]any{"name": "TOKEN", "value": "do-not-send"}}, "resources": map[string]any{"requests": map[string]any{"cpu": "100m", "memory": "64Mi"}}},
		}}},
	}
	deployment["metadata"].(map[string]any)["annotations"] = map[string]any{"private.example/note": "annotation-secret"}
	configMap := askInventoryObject("ConfigMap", "app-config", "payments", "config-1", nil)
	configMap["data"] = map[string]any{"APP_MODE": "production-secret", "LOG_LEVEL": "info"}
	reader := &askInventoryReader{items: map[string][]map[string]any{"deployments": {deployment}, "configmaps": {configMap}}}
	findings, skipped := askInventoryFindings(context.Background(), reader, options{namespace: "payments"}, []string{"deployment", "configmap"})
	if len(skipped) != 0 || len(findings) != 2 {
		t.Fatalf("inventory findings=%#v skipped=%#v", findings, skipped)
	}
	joined := ""
	for _, finding := range findings {
		for _, evidence := range finding.Evidence {
			joined += evidence.Label + ":" + evidence.Value + "\n"
		}
	}
	for _, want := range []string{"Configured replicas:2", "Container image:api=example/api:v1", "Container ports:api=http:8080/TCP", "Configured readiness probe:api=httpGet", "Container resource requests:api={\"cpu\":\"100m\",\"memory\":\"64Mi\"}", "ConfigMap keys:APP_MODE,LOG_LEVEL"} {
		if !strings.Contains(joined, want) {
			t.Errorf("resource context missing %q: %s", want, joined)
		}
	}
	for _, secret := range []string{"do-not-send", "annotation-secret", "production-secret", "TOKEN", "hidden-command"} {
		if strings.Contains(joined, secret) {
			t.Errorf("resource context leaked excluded object field %q: %s", secret, joined)
		}
	}
}

func TestAskMergesInventoryOnlyForResourcesWithoutFindings(t *testing.T) {
	active := analyzer.Finding{ID: "payments/Deployment/api/CrashLoopBackOff", Namespace: "payments", ResourceKind: "Deployment", ResourceName: "api", Status: "CrashLoopBackOff", Evidence: []analyzer.Evidence{{Label: "Exit code", Value: "1"}}}
	observed := analyzer.Finding{ID: "payments/Deployment/api/Observed", Namespace: "payments", ResourceKind: "deployment", ResourceName: "api", Status: "Observed", Evidence: []analyzer.Evidence{{Label: "Container image", Value: "api=example/api:v1"}}}
	got := mergeAskInventoryFindings([]analyzer.Finding{active}, []analyzer.Finding{observed, active})
	if len(got) != 1 || got[0].ID != active.ID || !reflect.DeepEqual(got[0].Evidence, []analyzer.Evidence{{Label: "Exit code", Value: "1"}, {Label: "Container image", Value: "api=example/api:v1"}}) {
		t.Fatalf("inventory duplicated an active finding: %#v", got)
	}
}

func askInventoryObject(kind, name, namespace, uid string, labels map[string]any) map[string]any {
	return map[string]any{"apiVersion": "v1", "kind": kind, "metadata": map[string]any{"name": name, "namespace": namespace, "uid": uid, "labels": labels}}
}

func boolString(value bool) string {
	if value {
		return "true"
	}
	return "false"
}

var _ kube.Reader = (*askInventoryReader)(nil)
