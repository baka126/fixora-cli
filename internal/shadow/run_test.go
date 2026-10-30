package shadow

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/fixora/kubectl-fixora/internal/analyzer"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
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

func TestOOMMemoryGateIsScopedAndFailsClosedOnMissingEvidence(t *testing.T) {
	finding := analyzer.Finding{Status: "OOMKilled", Evidence: []analyzer.Evidence{{Label: "Failing container", Value: "api"}}}
	attempt := Attempt{Ready: true, Phase: "Running", Evidence: &FailureEvidence{}}
	got := gateOOMMemoryAssessment(finding, &attempt)
	if got.Status != "inconclusive" || attempt.Ready || !attempt.Inconclusive {
		t.Fatalf("OOM with absent memory evidence must be inconclusive: assessment=%#v attempt=%#v", got, attempt)
	}

	nonOOM := Attempt{Ready: true, Phase: "Running", Evidence: &FailureEvidence{}}
	if got := gateOOMMemoryAssessment(analyzer.Finding{Status: "CrashLoopBackOff"}, &nonOOM); got != nil || !nonOOM.Ready || nonOOM.Inconclusive {
		t.Fatalf("non-OOM verification behavior changed: assessment=%#v attempt=%#v", got, nonOOM)
	}
}

func TestOOMMemoryGateMarksUnsafeCurveRetryableAndCandidateOOMFailed(t *testing.T) {
	finding := analyzer.Finding{Status: "OOMKilled", Evidence: []analyzer.Evidence{{Label: "Failing container", Value: "api"}}}
	unsafe := memoryEvidence(50*1024*1024, 55*1024*1024, 60*1024*1024)
	attempt := Attempt{Ready: true, Phase: "Running", Evidence: unsafe}
	assessment := gateOOMMemoryAssessment(finding, &attempt)
	if assessment == nil || assessment.Status != "failed" || attempt.Ready || attempt.Inconclusive || attempt.Message == "" {
		t.Fatalf("unsafe memory curve must become retryable failure: assessment=%#v attempt=%#v", assessment, attempt)
	}

	candidate := memoryEvidence(40*1024*1024, 40*1024*1024, 40*1024*1024)
	candidate.Containers[0].TerminationReason = "OOMKilled"
	attempt = Attempt{Ready: true, Phase: "Running", Evidence: candidate}
	assessment = gateOOMMemoryAssessment(finding, &attempt)
	if assessment == nil || assessment.Status != "failed" || attempt.Ready || attempt.Inconclusive {
		t.Fatalf("candidate OOM must remain failed regardless of measured headroom: assessment=%#v attempt=%#v", assessment, attempt)
	}
}

func TestNamespaceOwnershipMismatchBlocksVerifiedResult(t *testing.T) {
	actual := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "fixora-shadow-test", UID: "new-uid", Labels: map[string]string{labelSandbox: "true", labelSession: "run"}}}
	clientset := fake.NewSimpleClientset(actual)
	result := Result{Verified: true, Status: VerificationVerified}
	cleanup(context.Background(), &kube.TypedClient{Clientset: clientset}, clonePlan{Namespace: &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: actual.Name, UID: "old-uid", Labels: actual.Labels}}}, &result)
	if len(result.cleanupFailures()) == 0 {
		t.Fatalf("ownership mismatch must block delivery: %#v", result)
	}
	for _, action := range clientset.Actions() {
		if action.GetVerb() == "delete" {
			t.Fatalf("cleanup deleted namespace with changed UID: %#v", action)
		}
	}
}

func TestAmbiguousNamespaceCreateIsRecoveredByExactRunMarker(t *testing.T) {
	clientset := fake.NewSimpleClientset(&appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "prod"},
		Spec:       appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "api", Image: "repo/api:v1"}}}}},
	})
	createdName := ""
	clientset.PrependReactor("create", "namespaces", func(action ktesting.Action) (bool, runtime.Object, error) {
		ns := action.(ktesting.CreateAction).GetObject().(*corev1.Namespace).DeepCopy()
		createdName = ns.Name
		ns.UID = "uid-ambiguous"
		if err := clientset.Tracker().Create(corev1.SchemeGroupVersion.WithResource("namespaces"), ns, ""); err != nil {
			return true, nil, err
		}
		return true, nil, errors.New("connection reset after create")
	})
	patch := "spec:\n  template:\n    spec:\n      containers:\n      - name: api\n        image: repo/api:v2\n"
	_, err := Run(context.Background(), &kube.TypedClient{Clientset: clientset}, Request{Namespace: "prod", Resource: "Deployment/api", Patch: patch, ProbeImage: testIsolationProbeImage})
	if err == nil {
		t.Fatal("ambiguous namespace create should remain a blocked run")
	}
	if _, err := clientset.Tracker().Get(corev1.SchemeGroupVersion.WithResource("namespaces"), "", createdName); err == nil {
		t.Fatal("Fixora-owned namespace remained after create response was lost")
	}
}

func TestRequiredConfigMapMakesShadowInconclusiveWithoutCandidate(t *testing.T) {
	clientset := fake.NewSimpleClientset(&appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "prod"},
		Spec:       appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "api", Image: "repo/api:v1", Env: []corev1.EnvVar{{Name: "MODE", ValueFrom: &corev1.EnvVarSource{ConfigMapKeyRef: &corev1.ConfigMapKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "api-config"}, Key: "MODE"}}}}}}}}},
	})
	clientset.PrependReactor("create", "namespaces", func(action ktesting.Action) (bool, runtime.Object, error) {
		ns := action.(ktesting.CreateAction).GetObject().(*corev1.Namespace).DeepCopy()
		ns.UID = "uid-config-test"
		if err := clientset.Tracker().Create(corev1.SchemeGroupVersion.WithResource("namespaces"), ns, ""); err != nil {
			return true, nil, err
		}
		return true, ns, nil
	})
	result, err := Run(context.Background(), &kube.TypedClient{Clientset: clientset}, Request{
		Namespace: "prod", Resource: "Deployment/api", ProbeImage: testIsolationProbeImage,
		Patch: "spec:\n  template:\n    spec:\n      containers:\n      - name: api\n        image: repo/api:v2\n",
	})
	if err != nil || result.Status != VerificationInconclusive || result.Verified {
		t.Fatalf("required ConfigMap must make shadow inconclusive: result=%#v err=%v", result, err)
	}
	if result.Evidence == nil || len(result.Evidence.ConfigMaps) != 1 || result.Evidence.ConfigMaps[0].SourceStatus != "not-found" || result.Evidence.ConfigMaps[0].SandboxStatus != "omitted-by-isolation" {
		t.Fatalf("inconclusive result should preserve source ConfigMap key evidence: %#v", result.Evidence)
	}
	for _, action := range clientset.Actions() {
		if action.GetVerb() == "create" && (action.GetResource().Resource == "pods" || action.GetResource().Resource == "networkpolicies") {
			t.Fatalf("candidate resources created despite missing ConfigMap: %#v", action)
		}
	}
}

func TestKeepShadowRetainsOnlyTheOwnedRunNamespace(t *testing.T) {
	clientset, discoveryClient := isolationFake(true)
	if err := clientset.Tracker().Add(&appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "prod"},
		Spec:       appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "api", Image: "repo/api:v1"}}}}},
	}); err != nil {
		t.Fatal(err)
	}
	client := &kube.TypedClient{Clientset: clientset, Discovery: discoveryClient, Dynamic: dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())}
	result, err := Run(context.Background(), client, Request{
		Namespace: "prod", Resource: "Deployment/api", ProbeImage: testIsolationProbeImage, Keep: true, Timeout: time.Second,
		Patch: "spec:\n  template:\n    spec:\n      containers:\n      - name: api\n        image: repo/api:v2\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsolationNamespace == "" || len(result.Cleanup) != 0 {
		t.Fatalf("--keep-shadow should report one retained namespace: %#v", result)
	}
	if _, err := client.GetNamespace(context.Background(), result.IsolationNamespace); err != nil {
		t.Fatalf("retained namespace is missing: %v", err)
	}
	if _, err := client.Clientset.NetworkingV1().NetworkPolicies(result.IsolationNamespace).Get(context.Background(), result.NetworkPolicyName, metav1.GetOptions{}); err != nil {
		t.Fatalf("retained deny policy is missing: %v", err)
	}
	if _, err := client.GetTypedPod(context.Background(), result.IsolationNamespace, result.CloneName); err != nil {
		t.Fatalf("retained candidate is missing: %v status=%s isolation=%#v", err, result.Status, result.Isolation)
	}
}

func TestKeepShadowDoesNotRetainNamespaceWhenIsolationProofFails(t *testing.T) {
	clientset, discoveryClient := isolationFake(false)
	if err := clientset.Tracker().Add(&appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "prod"},
		Spec:       appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "api", Image: "repo/api:v1"}}}}},
	}); err != nil {
		t.Fatal(err)
	}
	client := &kube.TypedClient{Clientset: clientset, Discovery: discoveryClient, Dynamic: dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())}
	result, err := Run(context.Background(), client, Request{
		Namespace: "prod", Resource: "Deployment/api", ProbeImage: testIsolationProbeImage, Keep: true, Timeout: time.Second,
		Patch: "spec:\n  template:\n    spec:\n      containers:\n      - name: api\n        image: repo/api:v2\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != VerificationBlocked || result.Verified || len(result.Attempts) != 0 {
		t.Fatalf("failed isolation proof must block before candidate creation: %#v", result)
	}
	if _, err := client.GetNamespace(context.Background(), result.IsolationNamespace); !apierrors.IsNotFound(err) {
		t.Fatalf("--keep-shadow retained an unproven namespace %q: %v", result.IsolationNamespace, err)
	}
}

func TestShadowRejectsEgressAllowBeforeCreatingNamespace(t *testing.T) {
	clientset := fake.NewSimpleClientset(&appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "prod"},
		Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "api", Image: "repo/api:v1"}},
		}}},
	})
	patch := "spec:\n  template:\n    spec:\n      containers:\n      - name: api\n        image: repo/api:v2\n"
	_, err := Run(context.Background(), &kube.TypedClient{Clientset: clientset}, Request{Namespace: "prod", Resource: "Deployment/api", Egress: "allow", Patch: patch})
	if err == nil || !strings.Contains(err.Error(), "only supports deny egress") {
		t.Fatalf("expected allow egress to fail closed, got %v", err)
	}
	if got := len(clientset.Actions()); got != 0 {
		t.Fatalf("rejected request made Kubernetes API calls: %d actions", got)
	}
}

func TestShadowWithoutProbeImageBlocksBeforeCreatingResources(t *testing.T) {
	clientset := fake.NewSimpleClientset(&appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "prod"},
		Spec:       appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "api", Image: "repo/api:v1"}}}}},
	})
	result, err := Run(context.Background(), &kube.TypedClient{Clientset: clientset}, Request{
		Namespace: "prod", Resource: "Deployment/api", Timeout: time.Millisecond,
		Patch: "spec:\n  template:\n    spec:\n      containers:\n      - name: api\n        image: repo/api:v2\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != VerificationBlocked || result.Verified {
		t.Fatalf("missing probe image must block verification: %#v", result)
	}
	if len(clientset.Actions()) != 0 {
		t.Fatalf("missing probe image created Kubernetes resources: %#v", clientset.Actions())
	}
}

func TestRunDoesNotCreateCandidateWhenPolicyEnforcementCannotBeChecked(t *testing.T) {
	clientset := fake.NewSimpleClientset(&appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "prod"},
		Spec:       appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "api", Image: "repo/api:v1"}}}}},
	})
	clientset.PrependReactor("create", "namespaces", func(action ktesting.Action) (bool, runtime.Object, error) {
		ns := action.(ktesting.CreateAction).GetObject().(*corev1.Namespace).DeepCopy()
		ns.UID = "uid-enforcement-unknown"
		if err := clientset.Tracker().Create(corev1.SchemeGroupVersion.WithResource("namespaces"), ns, ""); err != nil {
			return true, nil, err
		}
		return true, ns, nil
	})
	result, err := Run(context.Background(), &kube.TypedClient{Clientset: clientset}, Request{
		Namespace: "prod", Resource: "Deployment/api", Timeout: time.Second, ProbeImage: testIsolationProbeImage,
		Patch: "spec:\n  template:\n    spec:\n      containers:\n      - name: api\n        image: repo/api:v2\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != VerificationBlocked || result.Verified || result.Isolation == nil {
		t.Fatalf("unproven enforcement must block the run: %#v", result)
	}
	for _, action := range clientset.Actions() {
		if action.GetVerb() == "create" && action.GetResource().Resource == "pods" {
			t.Fatalf("candidate or control pod created before policy support was proven: %#v", action)
		}
	}
}

func TestIsolationPreflightFailureReportsNamespaceCleanupFailure(t *testing.T) {
	clientset := fake.NewSimpleClientset(&appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "prod"},
		Spec:       appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "api", Image: "ghcr.io/acme/api:v1"}}}}},
	})
	clientset.PrependReactor("create", "namespaces", func(action ktesting.Action) (bool, runtime.Object, error) {
		ns := action.(ktesting.CreateAction).GetObject().(*corev1.Namespace).DeepCopy()
		ns.UID = "uid-run"
		if err := clientset.Tracker().Create(corev1.SchemeGroupVersion.WithResource("namespaces"), ns, ""); err != nil {
			return true, nil, err
		}
		return true, ns, nil
	})
	clientset.PrependReactor("delete", "namespaces", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("namespace deletion denied")
	})
	client := &kube.TypedClient{Clientset: clientset}
	result, err := Run(context.Background(), client, Request{
		Namespace: "prod", Resource: "Deployment/api", Timeout: time.Second, Egress: "deny", ProbeImage: testIsolationProbeImage,
		Patch: `spec:
  template:
    spec:
      containers:
      - name: api
        image: ghcr.io/acme/api:v2
`,
	})
	if err == nil || !strings.Contains(err.Error(), "namespace deletion denied") {
		t.Fatalf("namespace cleanup error lost: result=%#v err=%v", result, err)
	}
	for _, warning := range result.Warnings {
		if strings.Contains(warning, "cleanup failed for namespace/") && strings.Contains(warning, "namespace deletion denied") {
			return
		}
	}
	t.Fatalf("namespace cleanup error missing from result: %#v", result)
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
