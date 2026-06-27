package shadow

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

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
