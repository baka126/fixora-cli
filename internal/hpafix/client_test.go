package hpafix

import (
	"context"
	"testing"

	"github.com/fixora/kubectl-fixora/internal/kube"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
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
