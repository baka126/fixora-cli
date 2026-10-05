package shadow

import (
	"github.com/fixora/kubectl-fixora/internal/kube"
	"gopkg.in/yaml.v3"
	corev1 "k8s.io/api/core/v1"
	"testing"
)

func TestShadowProjectsSevenWorkloadKinds(t *testing.T) {
	for _, kind := range []string{"Pod", "Deployment", "StatefulSet", "DaemonSet", "ReplicaSet", "Job", "CronJob"} {
		t.Run(kind, func(t *testing.T) {
			obj, _ := kube.WorkloadPatch(kind, "app", "ns", map[string]any{"containers": []any{map[string]any{"name": "app", "image": "busybox:1.36"}}})
			data, _ := yaml.Marshal(obj)
			if err := ValidateCandidatePatch(string(data), "image", kind+"/app", "ns"); err != nil {
				t.Fatal(err)
			}
			pod := &corev1.Pod{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "busybox:1.35"}}, RestartPolicy: corev1.RestartPolicyOnFailure}}
			if err := applyPatchToPod(pod, string(data)); err != nil {
				t.Fatal(err)
			}
			if pod.Spec.Containers[0].Image != "busybox:1.36" || pod.Spec.RestartPolicy != corev1.RestartPolicyOnFailure {
				t.Fatalf("wrong projection: %+v", pod.Spec)
			}
		})
	}
}

func TestRevisionCannotAddUnverifiedMetadataOrStatus(t *testing.T) {
	original := "spec:\n  containers:\n  - name: app\n    image: busybox:1.35\n"
	for _, extra := range []string{"metadata: {finalizers: [example.com/hold]}\n", "status: {phase: Running}\n"} {
		revised := extra + "spec:\n  containers:\n  - name: app\n    image: busybox:1.36\n"
		if err := ValidateRevisedPatch(original, revised, "image"); err == nil {
			t.Fatalf("accepted unverified fields: %s", extra)
		}
	}
}
