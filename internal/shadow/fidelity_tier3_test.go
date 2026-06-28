package shadow

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/fixora/kubectl-fixora/internal/analyzer"
	"github.com/fixora/kubectl-fixora/internal/fix"
)

func warningsContain(warnings []string, needle string) bool {
	for _, w := range warnings {
		if strings.Contains(strings.ToLower(w), strings.ToLower(needle)) {
			return true
		}
	}
	return false
}

func TestCloneFidelityWarningsFlagsMeshInjectionAnnotation(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "api",
			Annotations: map[string]string{"sidecar.istio.io/inject": "true"},
		},
	}
	warnings := cloneFidelityWarnings(pod, "Deployment/api", nil)
	if !warningsContain(warnings, "service-mesh sidecar") {
		t.Fatalf("expected mesh sidecar warning, got %#v", warnings)
	}
}

func TestCloneFidelityWarningsFlagsMeshNamespaceLabel(t *testing.T) {
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "api"}}
	for _, label := range []map[string]string{
		{"istio-injection": "enabled"},
		{"linkerd.io/inject": "enabled"},
	} {
		warnings := cloneFidelityWarnings(pod, "Deployment/api", label)
		if !warningsContain(warnings, "service-mesh sidecar") {
			t.Fatalf("expected mesh sidecar warning for %v, got %#v", label, warnings)
		}
	}
}

func TestCloneFidelityWarningsNoMeshNoWarning(t *testing.T) {
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "api"}}
	warnings := cloneFidelityWarnings(pod, "Deployment/api", map[string]string{"team": "payments"})
	if warningsContain(warnings, "service-mesh sidecar") {
		t.Fatalf("did not expect mesh sidecar warning, got %#v", warnings)
	}
}

func TestCloneFidelityWarningsFlagsNonIdempotentJob(t *testing.T) {
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "batch"}}
	for _, kind := range []string{"Job/batch", "CronJob/batch"} {
		warnings := cloneFidelityWarnings(pod, kind, nil)
		if !warningsContain(warnings, "idempotent") {
			t.Fatalf("expected non-idempotent Job warning for %q, got %#v", kind, warnings)
		}
	}
}

func TestPartialPassCaveatsListsUnexercisedSurfaces(t *testing.T) {
	original := &corev1.Pod{Spec: corev1.PodSpec{
		Containers: []corev1.Container{{
			Name: "app",
			Env: []corev1.EnvVar{{
				Name:      "TOKEN",
				ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{}},
			}},
		}},
		Volumes: []corev1.Volume{{
			Name:         "data",
			VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "data"}},
		}},
	}}
	caveats := partialPassCaveats(original, map[string]string{"istio-injection": "enabled"})
	if !warningsContain(caveats, "secret") {
		t.Fatalf("expected secret caveat, got %#v", caveats)
	}
	if !warningsContain(caveats, "persistent") {
		t.Fatalf("expected PVC caveat, got %#v", caveats)
	}
	if !warningsContain(caveats, "mesh") {
		t.Fatalf("expected mesh caveat, got %#v", caveats)
	}
}

func TestPartialPassCaveatsEmptyForPlainPod(t *testing.T) {
	original := &corev1.Pod{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app"}}}}
	if caveats := partialPassCaveats(original, nil); len(caveats) != 0 {
		t.Fatalf("expected no caveats for a plain pod, got %#v", caveats)
	}
}

func TestDiagnoseFailureClassifiesProbeMisconfigOnCleanLogs(t *testing.T) {
	// Never ready, no recognized error signal in logs/events: this is a probe
	// misconfiguration, not a candidate regression.
	result := Result{Attempts: []Attempt{{
		Number:  1,
		Ready:   false,
		Phase:   "Running",
		Message: "shadow pod never became ready before the timeout",
		Logs:    []string{"server listening on :8080", "ready to accept connections"},
	}}}
	finding := analyzer.Finding{Status: "Unavailable"}
	plan := fix.Plan{Strategy: "adjust-probe"}

	diagnosis := DiagnoseFailure(result, finding, plan)
	if diagnosis.Class != FailureClassProbeMisconfig {
		t.Fatalf("expected probe-misconfig class, got %#v", diagnosis)
	}
	if !diagnosis.DeliveryBlocked {
		t.Fatalf("probe-misconfig must keep delivery blocked: %#v", diagnosis)
	}
}

func TestDiagnoseFailureKeepsCandidateRegressionWhenLogsShowError(t *testing.T) {
	result := Result{Attempts: []Attempt{{
		Number:  1,
		Ready:   false,
		Logs:    []string{"panic: runtime error: invalid memory address"},
		Message: "never ready",
	}}}
	finding := analyzer.Finding{Status: "Unavailable"}
	plan := fix.Plan{Strategy: "adjust-probe"}

	diagnosis := DiagnoseFailure(result, finding, plan)
	if diagnosis.Class == FailureClassProbeMisconfig {
		t.Fatalf("logs show a real error signal; must not be probe-misconfig: %#v", diagnosis)
	}
}

// Safety-contract regression: validateShadowSource still hard-blocks the same
// unsafe inputs, and the ApplyEligible gate is unchanged by these warn/diagnose
// additions (warnings/caveats lower stated confidence only).
func TestSafetyContractUnchangedByFidelityAdditions(t *testing.T) {
	privileged := true
	blocked := []struct {
		name string
		pod  *corev1.Pod
		want string
	}{
		{"pvc", &corev1.Pod{Spec: corev1.PodSpec{Volumes: []corev1.Volume{{Name: "d", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "d"}}}}}}, "persistent volume claim"},
		{"secret vol", &corev1.Pod{Spec: corev1.PodSpec{Volumes: []corev1.Volume{{Name: "s", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: "s"}}}}}}, "Secret volume"},
		{"secret env", &corev1.Pod{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "a", Env: []corev1.EnvVar{{Name: "T", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{}}}}}}}}, "Secret environment"},
		{"secretRef envFrom", &corev1.Pod{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "a", EnvFrom: []corev1.EnvFromSource{{SecretRef: &corev1.SecretEnvSource{}}}}}}}, "Secret environment"},
		{"hostNetwork", &corev1.Pod{Spec: corev1.PodSpec{HostNetwork: true}}, "host network"},
		{"hostPath", &corev1.Pod{Spec: corev1.PodSpec{Volumes: []corev1.Volume{{Name: "h", VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{}}}}}}, "hostPath"},
		{"privileged", &corev1.Pod{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "a", SecurityContext: &corev1.SecurityContext{Privileged: &privileged}}}}}, "privileged"},
		{"host port", &corev1.Pod{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "a", Ports: []corev1.ContainerPort{{HostPort: 8080}}}}}}, "host port"},
	}
	for _, tc := range blocked {
		t.Run(tc.name, func(t *testing.T) {
			if err := validateShadowSource(tc.pod); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected %q hard-block, got %v", tc.want, err)
			}
		})
	}

	// ApplyEligible is computed solely inside fix.BuildPlan/Concretize from the
	// finding+plan; shadow warnings/caveats never feed it. Assert the gate value
	// is identical whether or not fidelity caveats exist.
	finding := analyzer.Finding{
		ResourceKind: "Deployment",
		ResourceName: "api",
		Namespace:    "prod",
		Status:       "ExecFormatError",
	}
	base := fix.BuildPlan(finding)
	before := base.ApplyEligible

	// Producing fidelity caveats for the same source must not change the gate.
	original := &corev1.Pod{Spec: corev1.PodSpec{
		Volumes: []corev1.Volume{{Name: "data", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "data"}}}},
	}}
	_ = partialPassCaveats(original, map[string]string{"istio-injection": "enabled"})
	_ = cloneFidelityWarnings(original, "Job/api", map[string]string{"istio-injection": "enabled"})

	after := fix.BuildPlan(finding).ApplyEligible
	if before != after {
		t.Fatalf("ApplyEligible changed by fidelity additions: before=%v after=%v", before, after)
	}
}
