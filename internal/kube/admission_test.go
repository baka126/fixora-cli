package kube

import (
	"context"
	"testing"
)

type admissionReader struct {
	Reader
	items []map[string]any
}

func (r admissionReader) GetResourceItems(context.Context, string, bool, string) ([]map[string]any, error) {
	return r.items, nil
}
func admissionFixture() (map[string]any, map[string]any, []Event) {
	obj, _ := WorkloadPatch("Deployment", "worker", "demo", map[string]any{"containers": []any{map[string]any{"name": "app", "image": "image:v1"}}})
	obj["metadata"].(map[string]any)["uid"] = "owner-1"
	rs, _ := WorkloadPatch("ReplicaSet", "worker-new", "demo", map[string]any{"containers": []any{map[string]any{"name": "app", "image": "image:v1"}}})
	rs["metadata"].(map[string]any)["uid"] = "rs-1"
	rs["metadata"].(map[string]any)["generation"] = float64(2)
	rs["metadata"].(map[string]any)["ownerReferences"] = []any{map[string]any{"apiVersion": "apps/v1", "kind": "Deployment", "name": "worker", "uid": "owner-1", "controller": true}}
	rs["spec"].(map[string]any)["replicas"] = float64(1)
	rs["status"] = map[string]any{"observedGeneration": float64(2), "conditions": []any{map[string]any{"type": "ReplicaFailure", "status": "True", "reason": "FailedCreate", "message": "violates PodSecurity: runAsNonRoot != true"}}}
	events := []Event{{InvolvedObject: ObjectReference{Kind: "ReplicaSet", Namespace: "demo", Name: "worker-new", UID: "rs-1"}, Reason: "FailedCreate", Message: "violates PodSecurity: runAsNonRoot != true"}}
	return obj, rs, events
}
func TestAdmissionFailureLinkedToCurrentOwnedRevision(t *testing.T) {
	obj, rs, events := admissionFixture()
	got, err := CurrentAdmissionFailure(context.Background(), admissionReader{items: []map[string]any{rs}}, obj, events)
	if err != nil || got == nil {
		t.Fatalf("current failed revision missing: %v", err)
	}
	for _, bad := range []string{"template", "owner", "event", "healthy", "generation"} {
		t.Run(bad, func(t *testing.T) {
			obj, rs, events := admissionFixture()
			switch bad {
			case "template":
				spec, _ := WorkloadPodSpec(rs)
				spec["containers"].([]any)[0].(map[string]any)["image"] = "old:v1"
			case "owner":
				rs["metadata"].(map[string]any)["ownerReferences"].([]any)[0].(map[string]any)["uid"] = "replaced"
			case "event":
				events[0].InvolvedObject.UID = "historical"
			case "healthy":
				rs["status"].(map[string]any)["readyReplicas"] = float64(1)
			case "generation":
				rs["status"].(map[string]any)["observedGeneration"] = float64(1)
			}
			got, err := CurrentAdmissionFailure(context.Background(), admissionReader{items: []map[string]any{rs}}, obj, events)
			if err != nil || got != nil {
				t.Fatalf("stale/unrelated admission failure accepted: %+v %v", got, err)
			}
		})
	}
}
