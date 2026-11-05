package analyzer

import (
	"context"
	"testing"

	"github.com/fixora/kubectl-fixora/internal/kube"
)

type admissionEvidenceReader struct {
	kube.Reader
	obj, revision map[string]any
	events        []kube.Event
	pods          kube.PodList
}

func (r admissionEvidenceReader) GetPods(context.Context, string, bool) (kube.PodList, error) {
	return r.pods, nil
}
func (r admissionEvidenceReader) GetEvents(context.Context, string, string) ([]kube.Event, error) {
	return r.events, nil
}
func (r admissionEvidenceReader) GetResourceItems(context.Context, string, bool, string) ([]map[string]any, error) {
	return []map[string]any{r.revision}, nil
}
func (r admissionEvidenceReader) GetResource(_ context.Context, _, ref string) (map[string]any, error) {
	if ref == "replicaset/worker-new" {
		return r.revision, nil
	}
	return r.obj, nil
}
func TestOldHealthyPodDoesNotHideCurrentAdmissionFailure(t *testing.T) {
	obj, _ := kube.WorkloadPatch("Deployment", "worker", "demo", map[string]any{"containers": []any{map[string]any{"name": "app", "image": "image:v1"}}})
	obj["metadata"].(map[string]any)["uid"] = "owner-1"
	revision, _ := kube.WorkloadPatch("ReplicaSet", "worker-new", "demo", map[string]any{"containers": []any{map[string]any{"name": "app", "image": "image:v1"}}})
	revision["metadata"] = map[string]any{"name": "worker-new", "namespace": "demo", "uid": "rs-1", "generation": float64(2), "ownerReferences": []any{map[string]any{"kind": "Deployment", "name": "worker", "uid": "owner-1", "apiVersion": "apps/v1", "controller": true}}}
	revision["spec"].(map[string]any)["replicas"] = float64(1)
	revision["status"] = map[string]any{"observedGeneration": float64(2), "conditions": []any{map[string]any{"type": "ReplicaFailure", "reason": "FailedCreate", "status": "True", "message": "violates PodSecurity: runAsNonRoot != true"}}}
	reader := admissionEvidenceReader{obj: obj, revision: revision, events: []kube.Event{{Reason: "FailedCreate", Message: "violates PodSecurity: runAsNonRoot != true", InvolvedObject: kube.ObjectReference{Kind: "ReplicaSet", Name: "worker-new", Namespace: "demo", UID: "rs-1"}}}, pods: kube.PodList{Items: []kube.Pod{{Metadata: kube.ObjectMeta{Name: "old-healthy", Namespace: "demo", UID: "old-pod"}, Status: kube.PodStatus{Phase: "Running", Conditions: []kube.Condition{{Type: "Ready", Status: "True"}}, ContainerStatuses: []kube.ContainerStatus{{Name: "app", Ready: true}}}}}}}
	a := New(reader, Options{Namespace: "demo"})
	f, err := a.findingForController(context.Background(), obj, "Deployment", "worker", "demo")
	if err != nil || f.Status != "FailedCreate" || f.ResourceUID != "owner-1" {
		t.Fatalf("current denied revision hidden: %+v %v", f, err)
	}
	// A historical event does not override a now healthy revision.
	revision["status"].(map[string]any)["readyReplicas"] = float64(1)
	reader.pods = kube.PodList{}
	a = New(reader, Options{Namespace: "demo"})
	f, err = a.findingForController(context.Background(), obj, "Deployment", "worker", "demo")
	if err != nil || f.Status == "FailedCreate" {
		t.Fatalf("historical failure override: %+v %v", f, err)
	}
}
