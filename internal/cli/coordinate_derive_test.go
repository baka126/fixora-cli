package cli

import (
	"reflect"
	"sort"
	"testing"

	"github.com/fixora/kubectl-fixora/internal/coordinate"
	"github.com/fixora/kubectl-fixora/internal/fix"
)

func deploymentManifest() map[string]any {
	return map[string]any{
		"kind": "Deployment",
		"spec": map[string]any{
			"template": map[string]any{
				"metadata": map[string]any{"labels": map[string]any{"app": "web", "tier": "fe"}},
				"spec": map[string]any{
					"imagePullSecrets": []any{map[string]any{"name": "regcred"}},
					"containers": []any{map[string]any{
						"name": "web",
						"envFrom": []any{
							map[string]any{"configMapRef": map[string]any{"name": "web-cfg"}},
							map[string]any{"secretRef": map[string]any{"name": "web-sec"}},
						},
						"env": []any{map[string]any{"name": "K", "valueFrom": map[string]any{
							"configMapKeyRef": map[string]any{"name": "key-cfg"},
						}}},
					}},
					"volumes": []any{
						map[string]any{"configMap": map[string]any{"name": "vol-cfg"}},
						map[string]any{"secret": map[string]any{"secretName": "vol-sec"}},
						map[string]any{"persistentVolumeClaim": map[string]any{"claimName": "data-pvc"}},
					},
				},
			},
		},
	}
}

func sortedEqual(t *testing.T, got, want []string, label string) {
	t.Helper()
	g := append([]string{}, got...)
	w := append([]string{}, want...)
	sort.Strings(g)
	sort.Strings(w)
	if !reflect.DeepEqual(g, w) {
		t.Fatalf("%s: got %v want %v", label, got, want)
	}
}

func TestExtractReferencesDeployment(t *testing.T) {
	refs := extractReferences(deploymentManifest())
	sortedEqual(t, refs.ConfigMaps, []string{"web-cfg", "key-cfg", "vol-cfg"}, "configmaps")
	sortedEqual(t, refs.Secrets, []string{"web-sec", "vol-sec", "regcred"}, "secrets")
	sortedEqual(t, refs.PVCs, []string{"data-pvc"}, "pvcs")
	if refs.PodLabels["app"] != "web" || refs.PodLabels["tier"] != "fe" {
		t.Fatalf("pod labels: %v", refs.PodLabels)
	}
}

func TestExtractReferencesPod(t *testing.T) {
	pod := map[string]any{
		"kind":     "Pod",
		"metadata": map[string]any{"labels": map[string]any{"app": "solo"}},
		"spec": map[string]any{
			"volumes": []any{map[string]any{"configMap": map[string]any{"name": "pod-cfg"}}},
		},
	}
	refs := extractReferences(pod)
	sortedEqual(t, refs.ConfigMaps, []string{"pod-cfg"}, "pod configmaps")
	if refs.PodLabels["app"] != "solo" {
		t.Fatalf("pod labels: %v", refs.PodLabels)
	}
}

func TestExtractReferencesCronJob(t *testing.T) {
	cj := map[string]any{
		"kind": "CronJob",
		"spec": map[string]any{"jobTemplate": map[string]any{"spec": map[string]any{"template": map[string]any{
			"spec": map[string]any{"volumes": []any{map[string]any{"secret": map[string]any{"secretName": "cj-sec"}}}},
		}}}},
	}
	refs := extractReferences(cj)
	sortedEqual(t, refs.Secrets, []string{"cj-sec"}, "cronjob secrets")
}

func TestMatchingServices(t *testing.T) {
	services := []map[string]any{
		{"metadata": map[string]any{"name": "web-svc"}, "spec": map[string]any{"selector": map[string]any{"app": "web"}}},
		{"metadata": map[string]any{"name": "other"}, "spec": map[string]any{"selector": map[string]any{"app": "db"}}},
		{"metadata": map[string]any{"name": "headless"}, "spec": map[string]any{"selector": map[string]any{}}},
	}
	got := matchingServices(services, map[string]string{"app": "web", "tier": "fe"})
	sortedEqual(t, got, []string{"web-svc"}, "matching services")
}

func TestAssembleRefsOrder(t *testing.T) {
	refs := References{ConfigMaps: []string{"c1"}, Secrets: []string{"s1"}, PVCs: []string{"p1"}}
	got := assembleRefs(refs, []string{"svc1"}, "Deployment/web")
	want := []string{"ConfigMap/c1", "Secret/s1", "PersistentVolumeClaim/p1", "Deployment/web", "Service/svc1"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("order: got %v want %v", got, want)
	}
}

func TestFilterApplyEligible(t *testing.T) {
	steps := []coordinate.Step{
		{Ref: "ConfigMap/a", Plan: fix.Plan{ApplyEligible: true}},
		{Ref: "Deployment/b", Plan: fix.Plan{ApplyEligible: false}},
		{Ref: "Secret/c", Plan: fix.Plan{ApplyEligible: true}},
	}
	got := filterApplyEligible(steps)
	if len(got) != 2 || got[0].Ref != "ConfigMap/a" || got[1].Ref != "Secret/c" {
		t.Fatalf("filter dropped/ordered wrong: %#v", got)
	}
}
