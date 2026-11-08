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
			"apiVersion": "apps/v1", "kind": "Deployment",
			"metadata": map[string]any{"uid": "d", "name": "missing-config-demo", "namespace": "demo"},
			"spec":     map[string]any{"selector": map[string]any{"matchLabels": map[string]any{"app": "missing-config-demo"}}},
		},
		pods: kube.PodList{Items: []kube.Pod{{
			Metadata: kube.ObjectMeta{Name: "missing-config-demo-abc", Namespace: "demo", Labels: map[string]string{"app": "missing-config-demo"}, OwnerRefs: []kube.OwnerReference{{Kind: "ReplicaSet", Name: "test-rs", UID: "r", APIVersion: "apps/v1", Controller: boolPointer(true)}}},
			Status:   kube.PodStatus{Phase: "Pending", ContainerStatuses: []kube.ContainerStatus{{Name: "config-consumer", State: map[string]kube.StatusState{"waiting": {Reason: "CreateContainerConfigError"}}}}},
		}}},
		events: []kube.Event{{Metadata: kube.ObjectMeta{Namespace: "demo"}, InvolvedObject: kube.ObjectReference{Kind: "Pod", Name: "missing-config-demo-abc", Namespace: "demo"}, Reason: "Failed", Message: "missing-config-demo-abc: Secret missing-secret not found"}},
	}
	reader.owners = testDeploymentOwner(reader.resource)
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

func TestControllerHintsComeFromVerifiedOwner(t *testing.T) {
	reader := fakeReader{resource: map[string]any{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": map[string]any{"uid": "d", "name": "app", "namespace": "demo", "labels": map[string]any{"kustomize.toolkit.fluxcd.io/name": "workloads"}}}, pods: kube.PodList{Items: []kube.Pod{{Metadata: kube.ObjectMeta{Name: "app-pod", Namespace: "demo", UID: "p", OwnerRefs: []kube.OwnerReference{{Kind: "ReplicaSet", Name: "test-rs", UID: "r", APIVersion: "apps/v1", Controller: boolPointer(true)}}}, Status: kube.PodStatus{ContainerStatuses: []kube.ContainerStatus{{Name: "app", State: map[string]kube.StatusState{"waiting": {Reason: "ImagePullBackOff"}}}}}}}}}
	reader.owners = testDeploymentOwner(reader.resource)
	f, err := New(reader, Options{Namespace: "demo"}).AnalyzeResource(context.Background(), "deployment/app")
	if err != nil || f.GitOps.FluxHint == "" || f.ResourceUID != "d" || f.PodUID != "p" {
		t.Fatalf("verified owner proof lost: %+v %v", f, err)
	}
}
func TestControllerOwnershipIsNotGuessedFromLabels(t *testing.T) {
	reader := fakeReader{resource: map[string]any{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": map[string]any{"uid": "d", "name": "app", "namespace": "demo"}, "spec": map[string]any{"selector": map[string]any{"matchLabels": map[string]any{"app": "app"}}}}, pods: kube.PodList{Items: []kube.Pod{{Metadata: kube.ObjectMeta{Name: "app-guessed-prefix", Namespace: "demo", Labels: map[string]string{"app": "app"}}, Status: kube.PodStatus{ContainerStatuses: []kube.ContainerStatus{{State: map[string]kube.StatusState{"waiting": {Reason: "ImagePullBackOff"}}}}}}}}}
	f, err := New(reader, Options{Namespace: "demo"}).AnalyzeResource(context.Background(), "deployment/app")
	if err != nil || f.Status != "NoOwnedPods" {
		t.Fatalf("selector collision was treated as owned: %+v %v", f, err)
	}
}
