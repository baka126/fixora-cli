package analyzer

import (
	"context"
	"testing"

	"github.com/fixora/kubectl-fixora/internal/kube"
)

// Supplying a missing Secret is an operator action, not a generated patch.
// Once its consumer is Ready, historical failure events must not keep the
// Deployment diagnosed as broken.
func TestDemoSecretConsumerRecovery(t *testing.T) {
	reader := fakeReader{
		resource: map[string]any{
			"metadata": map[string]any{"name": "missing-config-demo", "namespace": "demo"},
			"spec":     map[string]any{"selector": map[string]any{"matchLabels": map[string]any{"app": "missing-config-demo"}}},
		},
		pods: kube.PodList{Items: []kube.Pod{{
			Metadata: kube.ObjectMeta{Name: "missing-config-demo-abc", Namespace: "demo", Labels: map[string]string{"app": "missing-config-demo"}},
			Status:   kube.PodStatus{Phase: "Pending", ContainerStatuses: []kube.ContainerStatus{{Name: "config-consumer", State: map[string]kube.StatusState{"waiting": {Reason: "CreateContainerConfigError"}}}}},
		}}},
		events: []kube.Event{{Metadata: kube.ObjectMeta{Namespace: "demo"}, InvolvedObject: kube.ObjectReference{Kind: "Pod", Name: "missing-config-demo-abc", Namespace: "demo"}, Reason: "Failed", Message: "missing-config-demo-abc: Secret missing-secret not found"}},
	}
	broken, err := New(reader, Options{Namespace: "demo"}).AnalyzeResource(context.Background(), "deployment/missing-config-demo")
	if err != nil {
		t.Fatal(err)
	}
	if broken.Status != "CreateContainerConfigError" {
		t.Fatalf("lost missing-config diagnosis: %+v", broken)
	}
	reader.pods.Items[0].Status.Phase = "Running"
	reader.pods.Items[0].Status.ContainerStatuses = []kube.ContainerStatus{{Name: "config-consumer", Ready: true, State: map[string]kube.StatusState{"running": {}}}}
	healthy, err := New(reader, Options{Namespace: "demo"}).AnalyzeResource(context.Background(), "deployment/missing-config-demo")
	if err != nil {
		t.Fatal(err)
	}
	if healthy.Status != "OwnedPodsHealthy" {
		t.Fatalf("historical Secret failure overrode the recovered workload: %+v", healthy)
	}
}
