package analyzer

import (
	"context"
	"testing"

	"github.com/fixora/kubectl-fixora/internal/kube"
)

func TestScanContextAppliesLabelSelectorToPodsAndResources(t *testing.T) {
	reader := fakeReader{
		pods: kube.PodList{Items: []kube.Pod{
			{Metadata: kube.ObjectMeta{Name: "api", Namespace: "prod", Labels: map[string]string{"app": "api", "tier": "web"}}},
			{Metadata: kube.ObjectMeta{Name: "cache", Namespace: "prod", Labels: map[string]string{"app": "cache", "tier": "cache"}}},
		}},
		items: map[string][]map[string]any{
			"services": {
				{"metadata": map[string]any{"name": "api", "namespace": "prod", "labels": map[string]any{"app": "api", "tier": "web"}}},
				{"metadata": map[string]any{"name": "cache", "namespace": "prod", "labels": map[string]any{"app": "cache", "tier": "cache"}}},
			},
		},
	}
	ctx := NewScanContext(context.Background(), reader, Options{Namespace: "prod", LabelSelector: "app=api,tier!=cache"})

	pods, err := ctx.GetPods()
	if err != nil {
		t.Fatal(err)
	}
	if len(pods.Items) != 1 || pods.Items[0].Metadata.Name != "api" {
		t.Fatalf("selector did not filter pods: %#v", pods.Items)
	}

	services, err := ctx.GetResourceItems("prod", false, "services")
	if err != nil {
		t.Fatal(err)
	}
	if len(services) != 1 || strValue(nestedMap(services[0], "metadata")["name"]) != "api" {
		t.Fatalf("selector did not filter services: %#v", services)
	}
}

func TestLabelSelectorSupportsExistenceAndNotExistence(t *testing.T) {
	labels := map[string]string{"app": "api", "track": "stable"}
	ok, err := labelsSatisfySelector(labels, "app,track==stable,!debug")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected selector to match labels")
	}
	ok, err = labelsSatisfySelector(labels, "debug")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("expected missing existence selector to fail")
	}
}

func TestInvalidLabelSelectorReturnsError(t *testing.T) {
	if _, err := labelsSatisfySelector(map[string]string{"app": "api"}, "app="); err == nil {
		t.Fatal("expected invalid label selector to return error")
	}
}
