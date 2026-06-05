package shadow

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/fixora/kubectl-fixora/internal/kube"
)

func TestCleanupReportsDeleteFailures(t *testing.T) {
	client := &kube.TypedClient{Clientset: fake.NewSimpleClientset()}
	plan := clonePlan{
		Clone:  &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "missing-pod", Namespace: "prod", Labels: shadowLabels()}},
		Policy: &networkingv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{Name: "missing-netpol", Namespace: "prod", Labels: shadowLabels()}},
	}
	var result Result
	cleanup(context.Background(), client, plan, &result)
	joined := strings.Join(result.Warnings, "\n")
	if !strings.Contains(joined, "cleanup failed for pod/missing-pod") || !strings.Contains(joined, "cleanup failed for networkpolicy/missing-netpol") {
		t.Fatalf("expected cleanup warnings, got %#v", result.Warnings)
	}
}

func TestCleanupSkipsNonShadowResources(t *testing.T) {
	client := &kube.TypedClient{Clientset: fake.NewSimpleClientset(
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "real-pod", Namespace: "prod"}},
		&networkingv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{Name: "real-netpol", Namespace: "prod"}},
	)}
	plan := clonePlan{
		Clone:  &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "real-pod", Namespace: "prod"}},
		Policy: &networkingv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{Name: "real-netpol", Namespace: "prod"}},
	}
	var result Result
	cleanup(context.Background(), client, plan, &result)
	if len(result.Cleanup) != 0 {
		t.Fatalf("non-shadow resources should not be deleted: %#v", result.Cleanup)
	}
	joined := strings.Join(result.Warnings, "\n")
	if !strings.Contains(joined, "cleanup skipped for pod/real-pod") || !strings.Contains(joined, "cleanup skipped for networkpolicy/real-netpol") {
		t.Fatalf("expected cleanup skip warnings, got %#v", result.Warnings)
	}
}

func shadowLabels() map[string]string {
	return map[string]string{labelSandbox: "true", labelSession: "test-session"}
}
