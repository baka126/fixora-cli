package shadow

import (
	"context"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/fixora/kubectl-fixora/internal/analyzer"
	"github.com/fixora/kubectl-fixora/internal/fix"
	"github.com/fixora/kubectl-fixora/internal/kube"
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
		Spec: corev1.PodSpec{NodeName: "node-a", NodeSelector: map[string]string{"pool": "workers"}, ServiceAccountName: "default"},
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
	if clone.Spec.NodeSelector["pool"] != "workers" {
		t.Fatalf("clone lost node selector: %#v", clone.Spec.NodeSelector)
	}
	if clone.Spec.ServiceAccountName != "" || clone.Spec.AutomountServiceAccountToken == nil || *clone.Spec.AutomountServiceAccountToken {
		t.Fatalf("clone retained workload credentials: %#v", clone.Spec)
	}
}

func TestValidateShadowSourceRejectsUnsafeOrStatefulInputs(t *testing.T) {
	privileged := true
	tests := []struct {
		name string
		pod  *corev1.Pod
		want string
	}{
		{name: "host path", pod: &corev1.Pod{Spec: corev1.PodSpec{Volumes: []corev1.Volume{{Name: "host", VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{}}}}}}, want: "hostPath"},
		{name: "pvc", pod: &corev1.Pod{Spec: corev1.PodSpec{Volumes: []corev1.Volume{{Name: "data", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "data"}}}}}}, want: "persistent volume claim"},
		{name: "privileged", pod: &corev1.Pod{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app", SecurityContext: &corev1.SecurityContext{Privileged: &privileged}}}}}, want: "privileged"},
		{name: "host port", pod: &corev1.Pod{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Ports: []corev1.ContainerPort{{HostPort: 8080}}}}}}, want: "host port"},
		{name: "external volume", pod: &corev1.Pod{Spec: corev1.PodSpec{Volumes: []corev1.Volume{{Name: "nfs", VolumeSource: corev1.VolumeSource{NFS: &corev1.NFSVolumeSource{Server: "nfs", Path: "/data"}}}}}}, want: "external or shared data"},
		{name: "service account", pod: &corev1.Pod{Spec: corev1.PodSpec{ServiceAccountName: "deployer"}}, want: "non-default service account"},
		{name: "secret env", pod: &corev1.Pod{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Env: []corev1.EnvVar{{Name: "TOKEN", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{}}}}}}}}, want: "Secret environment"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateShadowSource(tt.pod)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("expected %q rejection, got %v", tt.want, err)
			}
		})
	}
}

func TestPinCloneToOriginalPlatform(t *testing.T) {
	client := &kube.TypedClient{Clientset: fake.NewSimpleClientset(&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-a", Labels: map[string]string{"kubernetes.io/arch": "arm64"}}})}
	original := &corev1.Pod{Spec: corev1.PodSpec{NodeName: "node-a"}}
	clone := &corev1.Pod{Spec: corev1.PodSpec{NodeSelector: map[string]string{"pool": "workers"}}}
	warning, err := pinCloneToOriginalPlatform(context.Background(), client, original, clone)
	if err != nil {
		t.Fatal(err)
	}
	if clone.Spec.NodeSelector["kubernetes.io/arch"] != "arm64" || !strings.Contains(warning, "arm64") {
		t.Fatalf("expected architecture pin, clone=%#v warning=%q", clone.Spec.NodeSelector, warning)
	}
}

func TestSanitizePodStripsInjectedServiceAccountTokenVolume(t *testing.T) {
	pod := &corev1.Pod{Spec: corev1.PodSpec{
		Volumes: []corev1.Volume{{
			Name:         "kube-api-access",
			VolumeSource: corev1.VolumeSource{Projected: &corev1.ProjectedVolumeSource{Sources: []corev1.VolumeProjection{{ServiceAccountToken: &corev1.ServiceAccountTokenProjection{Path: "token"}}}}},
		}},
		Containers: []corev1.Container{{Name: "app", VolumeMounts: []corev1.VolumeMount{{Name: "kube-api-access", MountPath: "/var/run/secrets"}}}},
	}}
	clone := sanitizePod(pod, "12345678-1234-1234-1234-123456789abc", time.Minute)
	if len(clone.Spec.Volumes) != 0 || len(clone.Spec.Containers[0].VolumeMounts) != 0 {
		t.Fatalf("expected service account token volume and mounts to be stripped: %#v", clone.Spec)
	}
}

func TestBuildClonePlanUsesOwnedPodPlatformForDeploymentArchitectureFix(t *testing.T) {
	client := &kube.TypedClient{Clientset: fake.NewSimpleClientset(
		&appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "prod"},
			Spec:       appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "api", Image: "repo/api:v1"}}}}},
		},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "api-abc", Namespace: "prod"}, Spec: corev1.PodSpec{NodeName: "node-arm", Containers: []corev1.Container{{Name: "api", Image: "repo/api:v1"}}}},
		&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-arm", Labels: map[string]string{"kubernetes.io/arch": "arm64"}}},
	)}
	req := Request{
		Namespace: "prod",
		Resource:  "Deployment/api",
		Finding:   analyzer.Finding{PodName: "api-abc"},
		Plan:      fix.Plan{Strategy: "fix-architecture"},
		Patch: `spec:
  template:
    spec:
      containers:
      - name: api
        image: repo/api:v2
`,
	}
	plan, err := buildClonePlan(context.Background(), client, req, "12345678-1234-1234-1234-123456789abc")
	if err != nil {
		t.Fatal(err)
	}
	if plan.Clone.Spec.NodeSelector["kubernetes.io/arch"] != "arm64" {
		t.Fatalf("expected controller shadow clone to use failing pod architecture: %#v", plan.Clone.Spec.NodeSelector)
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
