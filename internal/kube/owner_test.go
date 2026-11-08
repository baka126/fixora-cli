package kube

import (
	"context"
	"fmt"
	"testing"
)

type ownerReader struct {
	Reader
	objects map[string]map[string]any
}

func (r ownerReader) GetResource(_ context.Context, ns, key string) (map[string]any, error) {
	o := r.objects[key]
	if o == nil {
		return nil, fmt.Errorf("missing")
	}
	return o, nil
}
func TestResolveWorkloadOwnerIdentity(t *testing.T) {
	yes := true
	pod := Pod{Metadata: ObjectMeta{Name: "app-pod", Namespace: "ns", UID: "p", OwnerRefs: []OwnerReference{{Kind: "ReplicaSet", Name: "arbitrary", UID: "r", Controller: &yes, APIVersion: "apps/v1"}}}}
	r := ownerReader{objects: map[string]map[string]any{
		"replicaset/arbitrary": {"apiVersion": "apps/v1", "kind": "ReplicaSet", "metadata": map[string]any{"name": "arbitrary", "namespace": "ns", "uid": "r", "ownerReferences": []any{map[string]any{"kind": "Deployment", "apiVersion": "apps/v1", "name": "app", "uid": "d", "controller": true}}}},
		"deployment/app":       {"apiVersion": "apps/v1", "kind": "Deployment", "metadata": map[string]any{"name": "app", "namespace": "ns", "uid": "d"}},
	}}
	target, err := ResolveWorkloadOwner(context.Background(), r, pod)
	if err != nil || target.Kind != "Deployment" || len(target.Chain) != 3 {
		t.Fatalf("%+v %v", target, err)
	}
	r.objects["deployment/app"]["metadata"].(map[string]any)["uid"] = "recreated"
	if _, err := ResolveWorkloadOwner(context.Background(), r, pod); err == nil {
		t.Fatal("accepted stale UID")
	}
	pod.Metadata.OwnerRefs = append(pod.Metadata.OwnerRefs, pod.Metadata.OwnerRefs[0])
	if _, err := ResolveWorkloadOwner(context.Background(), r, pod); err == nil {
		t.Fatal("accepted ambiguous owners")
	}
}

func TestOwnerResolutionIgnoresNonControllingReferences(t *testing.T) {
	yes := true
	pod := Pod{Metadata: ObjectMeta{Name: "pod", Namespace: "ns", OwnerRefs: []OwnerReference{{Kind: "ConfigMap", Name: "note"}, {Kind: "Job", Name: "job", UID: "j", APIVersion: "batch/v1", Controller: &yes}}}}
	r := ownerReader{objects: map[string]map[string]any{"job/job": {"kind": "Job", "apiVersion": "batch/v1", "metadata": map[string]any{"name": "job", "namespace": "ns", "uid": "j"}}}}
	got, err := ResolveWorkloadOwner(context.Background(), r, pod)
	if err != nil || got.Kind != "Job" {
		t.Fatalf("%+v %v", got, err)
	}
}
