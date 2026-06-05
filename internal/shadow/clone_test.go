package shadow

import (
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestSanitizePodStripsRoutingAndControllerMetadata(t *testing.T) {
	original := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "api-123",
			Namespace:       "prod",
			UID:             "uid",
			ResourceVersion: "42",
			Labels: map[string]string{
				"app":                    "api",
				"app.kubernetes.io/name": "api",
			},
			OwnerReferences: []metav1.OwnerReference{{Kind: "ReplicaSet", Name: "api-rs"}},
			Finalizers:      []string{"example.com/finalizer"},
		},
		Spec: corev1.PodSpec{NodeName: "node-a"},
	}
	clone := sanitizePod(original, "12345678-1234-1234-1234-123456789abc", time.Minute)
	if clone.UID != "" || clone.ResourceVersion != "" || len(clone.OwnerReferences) != 0 || len(clone.Finalizers) != 0 {
		t.Fatalf("clone kept controller metadata: %#v", clone.ObjectMeta)
	}
	if clone.Labels["app"] != "" || clone.Labels["app.kubernetes.io/name"] != "" {
		t.Fatalf("clone kept service selector labels: %#v", clone.Labels)
	}
	if clone.Labels[labelSandbox] != "true" || clone.Labels[labelSession] == "" {
		t.Fatalf("clone missing sandbox labels: %#v", clone.Labels)
	}
	if clone.Spec.NodeName != "" {
		t.Fatalf("clone kept node pinning: %q", clone.Spec.NodeName)
	}
}

func TestApplyWorkloadPatchProjectsTemplateSpecIntoPod(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "prod"},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{
			Name:  "api",
			Image: "old",
		}}},
	}
	patch := `apiVersion: apps/v1
kind: Deployment
metadata:
  name: api
  namespace: prod
spec:
  template:
    spec:
      containers:
      - name: api
        image: ghcr.io/acme/api:v2
`
	if err := applyPatchToPod(pod, patch); err != nil {
		t.Fatalf("apply patch: %v", err)
	}
	if got := pod.Spec.Containers[0].Image; got != "ghcr.io/acme/api:v2" {
		t.Fatalf("image = %q", got)
	}
}

func TestApplyPatchRejectsMultiDocumentYAML(t *testing.T) {
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "prod"}}
	err := applyPatchToPod(pod, "kind: Pod\n---\nkind: Service\n")
	if err == nil {
		t.Fatal("expected multi-document patch rejection")
	}
}

func TestSandboxNetworkPolicyBlocksIngressAndAllowsEgressByDefault(t *testing.T) {
	policy := sandboxNetworkPolicy("prod", "shadow-netpol", "session-a", "allow")
	if len(policy.Spec.Ingress) != 0 {
		t.Fatalf("expected ingress to be blocked, got %#v", policy.Spec.Ingress)
	}
	if len(policy.Spec.Egress) != 1 {
		t.Fatalf("expected one allow-all egress rule, got %#v", policy.Spec.Egress)
	}
	if policy.Spec.PodSelector.MatchLabels[labelSession] != "session-a" {
		t.Fatalf("policy selector = %#v", policy.Spec.PodSelector.MatchLabels)
	}
}
