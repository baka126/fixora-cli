package hpafix

import (
	"context"
	"testing"

	"github.com/fixora/kubectl-fixora/internal/kube"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic/fake"
	kubernetesfake "k8s.io/client-go/kubernetes/fake"
)

func TestTypedRuntimeResolvesDeploymentThroughReplicaSet(t *testing.T) {
	controller := true
	deployment := &appsv1.Deployment{TypeMeta: metav1.TypeMeta{APIVersion: "apps/v1", Kind: "Deployment"}, ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "demo", UID: "deployment-uid"}}
	replicaset := &appsv1.ReplicaSet{TypeMeta: metav1.TypeMeta{APIVersion: "apps/v1", Kind: "ReplicaSet"}, ObjectMeta: metav1.ObjectMeta{Name: "api-abc", Namespace: "demo", UID: "replicaset-uid", OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "Deployment", Name: "api", UID: "deployment-uid", Controller: &controller}}}}
	clientset := kubernetesfake.NewSimpleClientset(deployment, replicaset)
	dynamicClient := fake.NewSimpleDynamicClient(runtime.NewScheme())
	r := &typedRuntime{k: &kube.TypedClient{Clientset: clientset, Dynamic: dynamicClient}}
	pod := corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "api-abc-xyz", Namespace: "demo", UID: "pod-uid", OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: "api-abc", UID: "replicaset-uid", Controller: &controller}}}}

	owner, err := r.ResolveOwner(context.Background(), pod)
	if err != nil {
		t.Fatalf("ResolveOwner() error = %v", err)
	}
	if !owner.Matches("Deployment", "api", "deployment-uid") {
		t.Fatalf("ResolveOwner() = %#v, want verified Deployment/api owner", owner)
	}
}

func TestTypedRuntimeIgnoresOrdinaryJobAndDaemonSetOwners(t *testing.T) {
	controller := true
	job := &batchv1.Job{TypeMeta: metav1.TypeMeta{APIVersion: "batch/v1", Kind: "Job"}, ObjectMeta: metav1.ObjectMeta{Name: "batch", Namespace: "demo", UID: "job-uid"}}
	daemonset := &appsv1.DaemonSet{TypeMeta: metav1.TypeMeta{APIVersion: "apps/v1", Kind: "DaemonSet"}, ObjectMeta: metav1.ObjectMeta{Name: "agent", Namespace: "demo", UID: "ds-uid"}}
	clientset := kubernetesfake.NewSimpleClientset(job, daemonset)
	r := &typedRuntime{k: &kube.TypedClient{Clientset: clientset, Dynamic: fake.NewSimpleDynamicClient(runtime.NewScheme())}}
	for _, tc := range []struct{ kind, name, apiVersion, uid string }{{"Job", "batch", "batch/v1", "job-uid"}, {"DaemonSet", "agent", "apps/v1", "ds-uid"}} {
		pod := corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: tc.name + "-pod", Namespace: "demo", UID: types.UID(tc.uid + "-pod"), OwnerReferences: []metav1.OwnerReference{{APIVersion: tc.apiVersion, Kind: tc.kind, Name: tc.name, UID: types.UID(tc.uid), Controller: &controller}}}}
		owner, err := r.ResolveOwner(context.Background(), pod)
		if err != nil {
			t.Fatalf("ResolveOwner(%s) error = %v", tc.kind, err)
		}
		if owner.Kind != tc.kind || owner.Name != tc.name || owner.UID != tc.uid {
			t.Fatalf("ResolveOwner(%s) = %#v", tc.kind, owner)
		}
	}
}
