package shadow

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"

	"github.com/fixora/kubectl-fixora/internal/kube"
)

func TestResourceAllowsCompletionOnlyForBatchControllers(t *testing.T) {
	for _, resource := range []string{"Job/migrate", "CronJob/nightly", "cj/cleanup"} {
		if !resourceAllowsCompletion(resource) {
			t.Fatalf("expected %s to allow successful completion", resource)
		}
	}
	for _, resource := range []string{"Pod/api", "Deployment/api", "StatefulSet/db", "DaemonSet/agent"} {
		if resourceAllowsCompletion(resource) {
			t.Fatalf("expected %s to require readiness", resource)
		}
	}
}

func TestPodCreationFailureReportsNetworkPolicyCleanupFailure(t *testing.T) {
	clientset := fake.NewSimpleClientset(&appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "prod"},
		Spec:       appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "api", Image: "ghcr.io/acme/api:v1"}}}}},
	})
	clientset.PrependReactor("create", "pods", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("pod admission denied")
	})
	clientset.PrependReactor("delete", "networkpolicies", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("policy deletion denied")
	})
	client := &kube.TypedClient{Clientset: clientset}
	result, err := Run(context.Background(), client, Request{
		Namespace: "prod", Resource: "Deployment/api", Timeout: time.Second, Egress: "deny",
		Patch: `spec:
  template:
    spec:
      containers:
      - name: api
        image: ghcr.io/acme/api:v2
`,
	})
	if err == nil || !strings.Contains(err.Error(), "pod admission denied") {
		t.Fatalf("pod creation error lost: result=%#v err=%v", result, err)
	}
	for _, warning := range result.Warnings {
		if strings.Contains(warning, "cleanup failed for networkpolicy/") && strings.Contains(warning, "policy deletion denied") {
			return
		}
	}
	t.Fatalf("NetworkPolicy cleanup error missing from result: %#v", result)
}

func TestVerifyCloneAcceptsSuccessfulJobCompletion(t *testing.T) {
	client := &kube.TypedClient{Clientset: fake.NewSimpleClientset(&corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "migration", Namespace: "prod"},
		Status:     corev1.PodStatus{Phase: corev1.PodSucceeded},
	})}
	attempt := verifyClone(context.Background(), client, "prod", "migration", time.Second, 1, true)
	if !attempt.Ready || attempt.Message != "shadow batch pod completed successfully" {
		t.Fatalf("expected successful batch verification, got %#v", attempt)
	}
}
