package shadow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/fixora/kubectl-fixora/internal/kube"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	corefake "k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

func TestFailureEvidenceResolvesConfigMapKeysWithoutValues(t *testing.T) {
	secretLookingValue := "private-setting-value"
	original := &corev1.Pod{Spec: corev1.PodSpec{Containers: []corev1.Container{{
		Name: "api",
		Env: []corev1.EnvVar{{Name: "DATABASE_URL", ValueFrom: &corev1.EnvVarSource{ConfigMapKeyRef: &corev1.ConfigMapKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{Name: "api-config"}, Key: "DATABASE_URL",
		}}}},
	}}}}
	client := &kube.TypedClient{Clientset: corefake.NewSimpleClientset(&corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "api-config", Namespace: "prod"},
		Data:       map[string]string{"DATABASE_URL": secretLookingValue},
	})}
	evidence := collectFailureEvidence(context.Background(), client, "prod", original, nil)
	if len(evidence.ConfigMaps) != 1 || evidence.ConfigMaps[0].SourceStatus != "key-present" {
		t.Fatalf("want source key presence evidence, got %#v", evidence.ConfigMaps)
	}
	if evidence.ConfigMaps[0].SandboxStatus != "omitted-by-isolation" {
		t.Fatalf("want isolation omission status, got %#v", evidence.ConfigMaps[0])
	}
	encoded, err := json.Marshal(evidence)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), secretLookingValue) {
		t.Fatalf("ConfigMap values must never be included in evidence: %s", encoded)
	}
}

func TestPrepareShadowConfigMapsCopiesOnlyReferencedDataKeys(t *testing.T) {
	const privateValue = "private-setting-value"
	clientset := corefake.NewSimpleClientset(&corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "api-config", Namespace: "prod", Labels: map[string]string{"team": "payments"}},
		Data:       map[string]string{"DATABASE_URL": privateValue, "UNUSED": "do-not-copy"},
	})
	client := &kube.TypedClient{Clientset: clientset}
	pod := &corev1.Pod{Spec: corev1.PodSpec{Containers: []corev1.Container{{
		Name: "api",
		Env: []corev1.EnvVar{{Name: "DATABASE_URL", ValueFrom: &corev1.EnvVarSource{ConfigMapKeyRef: &corev1.ConfigMapKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{Name: "api-config"}, Key: "DATABASE_URL",
		}}}},
	}}}}
	prepared, err := prepareShadowConfigMaps(context.Background(), client, "prod", "shadow", pod)
	if err != nil {
		t.Fatal(err)
	}
	if !prepared.Copied || len(prepared.Limitations) != 0 || len(prepared.Evidence) != 1 {
		t.Fatalf("want one prepared key and no limitations, got %#v", prepared)
	}
	shadowMap, err := clientset.CoreV1().ConfigMaps("shadow").Get(context.Background(), "api-config", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("shadow ConfigMap was not created: %v", err)
	}
	if len(shadowMap.Data) != 1 || shadowMap.Data["DATABASE_URL"] != privateValue || len(shadowMap.Labels) != 1 || shadowMap.Labels[labelSandbox] != "true" {
		t.Fatalf("shadow ConfigMap must contain only referenced key and no source metadata: %#v", shadowMap)
	}
	evidence := prepared.Evidence[0]
	if evidence.EnvironmentVariable != "DATABASE_URL" || evidence.SourceStatus != "key-present" || evidence.SandboxStatus != "key-present" {
		t.Fatalf("expected source and sandbox key-presence evidence: %#v", evidence)
	}
	encoded, err := json.Marshal(prepared.Evidence)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), privateValue) || strings.Contains(string(encoded), "do-not-copy") {
		t.Fatalf("ConfigMap data leaked into evidence: %s", encoded)
	}
}

func TestPrepareShadowConfigMapsDetectsAdmissionMutation(t *testing.T) {
	const privateValue = "private-setting-value"
	clientset := corefake.NewSimpleClientset(&corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "api-config", Namespace: "prod"},
		Data:       map[string]string{"DATABASE_URL": privateValue},
	})
	clientset.PrependReactor("create", "configmaps", func(action ktesting.Action) (bool, runtime.Object, error) {
		created := action.(ktesting.CreateAction).GetObject().(*corev1.ConfigMap).DeepCopy()
		created.Data["DATABASE_URL"] = "mutated-value"
		created.Data["INJECTED"] = "extra-value"
		return true, created, nil
	})
	client := &kube.TypedClient{Clientset: clientset}
	pod := &corev1.Pod{Spec: corev1.PodSpec{Containers: []corev1.Container{{
		Name: "api",
		Env: []corev1.EnvVar{{Name: "DATABASE_URL", ValueFrom: &corev1.EnvVarSource{ConfigMapKeyRef: &corev1.ConfigMapKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{Name: "api-config"}, Key: "DATABASE_URL",
		}}}},
	}}}}
	prepared, err := prepareShadowConfigMaps(context.Background(), client, "prod", "shadow", pod)
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Copied || len(prepared.Limitations) == 0 || prepared.Evidence[0].SandboxStatus != "mismatch" {
		t.Fatalf("mutated admitted ConfigMap must remain inconclusive: %#v", prepared)
	}
	encoded, _ := json.Marshal(prepared)
	if strings.Contains(string(encoded), privateValue) || strings.Contains(string(encoded), "mutated-value") || strings.Contains(string(encoded), "extra-value") {
		t.Fatalf("admission values leaked into evidence: %s", encoded)
	}
}

func TestPrepareShadowConfigMapsMergesReferencesAndCapsSize(t *testing.T) {
	clientset := corefake.NewSimpleClientset(&corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "shared", Namespace: "prod"},
		Data:       map[string]string{"A": "value-a", "B": "value-b"},
	})
	client := &kube.TypedClient{Clientset: clientset}
	pod := &corev1.Pod{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "api", Env: []corev1.EnvVar{
		{Name: "A_ENV", ValueFrom: &corev1.EnvVarSource{ConfigMapKeyRef: &corev1.ConfigMapKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "shared"}, Key: "A"}}},
		{Name: "B_ENV", ValueFrom: &corev1.EnvVarSource{ConfigMapKeyRef: &corev1.ConfigMapKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "shared"}, Key: "B"}}},
	}}}}}
	prepared, err := prepareShadowConfigMaps(context.Background(), client, "prod", "shadow", pod)
	if err != nil || len(prepared.Evidence) != 2 {
		t.Fatalf("multiple direct refs should prepare, evidence=%#v err=%v", prepared, err)
	}
	objects, err := clientset.CoreV1().ConfigMaps("shadow").List(context.Background(), metav1.ListOptions{})
	if err != nil || len(objects.Items) != 1 || len(objects.Items[0].Data) != 2 {
		t.Fatalf("same-name references should merge into one minimal ConfigMap: items=%#v err=%v", objects, err)
	}

	keys := make([]string, 33)
	data := make(map[string]string, len(keys))
	envs := make([]corev1.EnvVar, len(keys))
	for i := range keys {
		keys[i] = fmt.Sprintf("K%d", i)
		data[keys[i]] = "v"
		envs[i] = corev1.EnvVar{Name: "E" + keys[i], ValueFrom: &corev1.EnvVarSource{ConfigMapKeyRef: &corev1.ConfigMapKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "large"}, Key: keys[i]}}}
	}
	largeClientset := corefake.NewSimpleClientset(&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "large", Namespace: "prod"}, Data: data})
	largeClient := &kube.TypedClient{Clientset: largeClientset}
	prepared, err = prepareShadowConfigMaps(context.Background(), largeClient, "prod", "shadow", &corev1.Pod{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "api", Env: envs}}}})
	if err != nil || len(prepared.Limitations) == 0 || prepared.Copied {
		t.Fatalf("more than 32 keys must be inconclusive before copying, prepared=%#v err=%v", prepared, err)
	}
	objects, _ = largeClientset.CoreV1().ConfigMaps("shadow").List(context.Background(), metav1.ListOptions{})
	if len(objects.Items) != 0 {
		t.Fatalf("key cap must prevent partial ConfigMap writes: %#v", objects.Items)
	}

	tooLargeClientset := corefake.NewSimpleClientset(&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "large-value", Namespace: "prod"}, Data: map[string]string{"K": strings.Repeat("x", 64*1024+1)}})
	tooLargeClient := &kube.TypedClient{Clientset: tooLargeClientset}
	tooLargePod := &corev1.Pod{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "api", Env: []corev1.EnvVar{{Name: "E", ValueFrom: &corev1.EnvVarSource{ConfigMapKeyRef: &corev1.ConfigMapKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "large-value"}, Key: "K"}}}}}}}}
	prepared, err = prepareShadowConfigMaps(context.Background(), tooLargeClient, "prod", "shadow", tooLargePod)
	if err != nil || len(prepared.Limitations) == 0 || prepared.Copied {
		t.Fatalf("more than 64KiB must be inconclusive before copying, prepared=%#v err=%v", prepared, err)
	}
}

func TestPrepareShadowConfigMapsRejectsUnsupportedDependencies(t *testing.T) {
	clientset := corefake.NewSimpleClientset(&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "api-config", Namespace: "prod"}, Data: map[string]string{"MODE": "prod"}})
	client := &kube.TypedClient{Clientset: clientset}
	optional := true
	tests := map[string]*corev1.Pod{
		"optional key": {Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "api", Env: []corev1.EnvVar{{Name: "MODE", ValueFrom: &corev1.EnvVarSource{ConfigMapKeyRef: &corev1.ConfigMapKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "api-config"}, Key: "MODE", Optional: &optional}}}}}}}},
		"envFrom":      {Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "api", EnvFrom: []corev1.EnvFromSource{{ConfigMapRef: &corev1.ConfigMapEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "api-config"}}}}}}}},
		"volume":       {Spec: corev1.PodSpec{Volumes: []corev1.Volume{{Name: "config", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: "api-config"}}}}}}},
	}
	for name, pod := range tests {
		t.Run(name, func(t *testing.T) {
			prepared, err := prepareShadowConfigMaps(context.Background(), client, "prod", "shadow-"+strings.ReplaceAll(name, " ", "-"), pod)
			if err != nil || len(prepared.Limitations) == 0 || prepared.Copied {
				t.Fatalf("unsupported dependency must fail closed: prepared=%#v err=%v", prepared, err)
			}
		})
	}
}

func TestPrepareShadowConfigMapsMissingKeyIsInconclusiveWithoutPartialCopy(t *testing.T) {
	clientset := corefake.NewSimpleClientset(&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "api-config", Namespace: "prod"}, Data: map[string]string{"OTHER": "hidden"}})
	client := &kube.TypedClient{Clientset: clientset}
	pod := &corev1.Pod{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "api", Env: []corev1.EnvVar{{Name: "MODE", ValueFrom: &corev1.EnvVarSource{ConfigMapKeyRef: &corev1.ConfigMapKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "api-config"}, Key: "MODE"}}}}}}}}
	prepared, err := prepareShadowConfigMaps(context.Background(), client, "prod", "shadow", pod)
	if err != nil || len(prepared.Limitations) == 0 || prepared.Copied || prepared.Evidence[0].SourceStatus != "key-missing" || prepared.Evidence[0].SandboxStatus != "unresolved" {
		t.Fatalf("missing key must remain inconclusive: prepared=%#v err=%v", prepared, err)
	}
	objects, _ := clientset.CoreV1().ConfigMaps("shadow").List(context.Background(), metav1.ListOptions{})
	if len(objects.Items) != 0 {
		t.Fatalf("missing key must not create partial ConfigMaps: %#v", objects.Items)
	}
}

func TestPrepareShadowConfigMapsReadErrorIsValueFreeLimitation(t *testing.T) {
	clientset := corefake.NewSimpleClientset()
	clientset.PrependReactor("get", "configmaps", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("backend echoed token=private-value")
	})
	client := &kube.TypedClient{Clientset: clientset}
	pod := &corev1.Pod{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "api", Env: []corev1.EnvVar{{Name: "MODE", ValueFrom: &corev1.EnvVarSource{ConfigMapKeyRef: &corev1.ConfigMapKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "api-config"}, Key: "MODE"}}}}}}}}
	prepared, err := prepareShadowConfigMaps(context.Background(), client, "prod", "shadow", pod)
	if err != nil || len(prepared.Limitations) == 0 {
		t.Fatalf("ConfigMap read uncertainty must be inconclusive, prepared=%#v err=%v", prepared, err)
	}
	encoded, _ := json.Marshal(prepared)
	if strings.Contains(string(encoded), "private-value") {
		t.Fatalf("ConfigMap read error leaked into evidence: %s", encoded)
	}
	objects, _ := clientset.CoreV1().ConfigMaps("shadow").List(context.Background(), metav1.ListOptions{})
	if len(objects.Items) != 0 {
		t.Fatalf("ConfigMap read error must not create partial copies: %#v", objects.Items)
	}
}

func TestFailureEvidenceReportsMissingConfigMapKeyWithoutValue(t *testing.T) {
	original := &corev1.Pod{Spec: corev1.PodSpec{Containers: []corev1.Container{{
		Name: "api",
		Env: []corev1.EnvVar{{Name: "DATABASE_URL", ValueFrom: &corev1.EnvVarSource{ConfigMapKeyRef: &corev1.ConfigMapKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{Name: "api-config"}, Key: "DATABASE_URL",
		}}}},
	}}}}
	client := &kube.TypedClient{Clientset: corefake.NewSimpleClientset(&corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "api-config", Namespace: "prod"},
		Data:       map[string]string{"OTHER_KEY": "irrelevant"},
	})}
	evidence := collectFailureEvidence(context.Background(), client, "prod", original, nil)
	if len(evidence.ConfigMaps) != 1 || evidence.ConfigMaps[0].SourceStatus != "key-missing" {
		t.Fatalf("want missing-key evidence, got %#v", evidence.ConfigMaps)
	}
	encoded, _ := json.Marshal(evidence)
	if strings.Contains(string(encoded), "irrelevant") {
		t.Fatalf("ConfigMap values must never be included in evidence: %s", encoded)
	}
}

func TestFailureEvidenceDoesNotTreatBinaryConfigMapKeyAsEnvironmentKey(t *testing.T) {
	original := &corev1.Pod{Spec: corev1.PodSpec{Containers: []corev1.Container{{
		Name: "api",
		Env: []corev1.EnvVar{{Name: "MODE", ValueFrom: &corev1.EnvVarSource{ConfigMapKeyRef: &corev1.ConfigMapKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{Name: "api-config"}, Key: "MODE",
		}}}},
	}}}}
	client := &kube.TypedClient{Clientset: corefake.NewSimpleClientset(&corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "api-config", Namespace: "prod"},
		BinaryData: map[string][]byte{"MODE": []byte("binary")},
	})}
	evidence := collectFailureEvidence(context.Background(), client, "prod", original, nil)
	if len(evidence.ConfigMaps) != 1 || evidence.ConfigMaps[0].SourceStatus != "key-missing" {
		t.Fatalf("binary ConfigMap key cannot satisfy an env key reference, got %#v", evidence.ConfigMaps)
	}
}

func TestFailureEvidenceAcceptsBinaryConfigMapKeyForVolume(t *testing.T) {
	original := &corev1.Pod{Spec: corev1.PodSpec{Volumes: []corev1.Volume{{Name: "config", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{
		LocalObjectReference: corev1.LocalObjectReference{Name: "api-config"}, Items: []corev1.KeyToPath{{Key: "settings", Path: "settings"}},
	}}}}}}
	client := &kube.TypedClient{Clientset: corefake.NewSimpleClientset(&corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "api-config", Namespace: "prod"},
		BinaryData: map[string][]byte{"settings": []byte("binary")},
	})}
	evidence := collectFailureEvidence(context.Background(), client, "prod", original, nil)
	if len(evidence.ConfigMaps) != 1 || evidence.ConfigMaps[0].SourceStatus != "key-present" {
		t.Fatalf("volume projection should accept a BinaryData key, got %#v", evidence.ConfigMaps)
	}
}

func TestFailureEvidenceRedactsProbePathQueryAndPriorTermination(t *testing.T) {
	pod := &corev1.Pod{
		Spec:   corev1.PodSpec{Containers: []corev1.Container{{Name: "api", Image: "registry/app:v2", ReadinessProbe: &corev1.Probe{ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: "/ready?token=hunter2", Port: intstr.FromInt(8080)}}}}}},
		Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{Name: "api", State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}}, LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "OOMKilled", ExitCode: 137}}}}},
	}
	evidence := collectFailureEvidence(context.Background(), nil, "prod", nil, pod)
	if len(evidence.Probes) != 1 || strings.Contains(evidence.Probes[0].Path, "hunter2") || strings.Contains(evidence.Probes[0].Path, "?") {
		t.Fatalf("probe path query must not be retained: %#v", evidence.Probes)
	}
	if evidence.Containers[0].TerminationReason != "OOMKilled" || evidence.Containers[0].ExitCode == nil || *evidence.Containers[0].ExitCode != 137 {
		t.Fatalf("last container termination should be preserved while waiting: %#v", evidence.Containers[0])
	}
}

func TestFailureEvidenceCapturesImageProbeAndReadinessTiming(t *testing.T) {
	created := metav1.NewTime(time.Now().Add(-20 * time.Second))
	readyAt := metav1.NewTime(created.Add(8 * time.Second))
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "api-shadow", Namespace: "sandbox", CreationTimestamp: created},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{
			Name: "api", Image: "registry/app:v2", Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("64Mi")}, Limits: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("128Mi")}},
			ReadinessProbe: &corev1.Probe{ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: "/ready", Port: intstr.FromInt(8080), Host: "internal.example", Scheme: corev1.URISchemeHTTP}}},
		}}},
		Status: corev1.PodStatus{Phase: corev1.PodRunning, Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue, LastTransitionTime: readyAt}}, ContainerStatuses: []corev1.ContainerStatus{{Name: "api", Image: "registry/app:v2", ImageID: "containerd://sha256:abc", Ready: true}}},
	}
	evidence := collectFailureEvidence(context.Background(), &kube.TypedClient{}, "prod", nil, pod)
	if len(evidence.Containers) != 1 || evidence.Containers[0].ImageID != "containerd://sha256:abc" || evidence.Containers[0].MemoryLimit != "128Mi" {
		t.Fatalf("image/runtime/resource evidence missing: %#v", evidence.Containers)
	}
	if len(evidence.Probes) != 1 || evidence.Probes[0].Path != "/ready" || evidence.Probes[0].Port != "8080" {
		t.Fatalf("probe evidence should include endpoint: %#v", evidence.Probes)
	}
	encoded, _ := json.Marshal(evidence)
	if strings.Contains(string(encoded), "internal.example") {
		t.Fatalf("probe evidence must not include hostnames: %s", encoded)
	}
	if evidence.TimeToReadySeconds == nil || *evidence.TimeToReadySeconds != 8 {
		t.Fatalf("want 8-second time-to-ready, got %#v", evidence.TimeToReadySeconds)
	}
}

func TestSamplePodMemoryMetricsReadsOnlyContainerMemory(t *testing.T) {
	metric := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "metrics.k8s.io/v1beta1", "kind": "PodMetrics",
		"metadata":   map[string]any{"name": "api-shadow", "namespace": "sandbox"},
		"containers": []any{map[string]any{"name": "api", "usage": map[string]any{"memory": "18Mi", "cpu": "250m"}}},
	}}
	dynamicClient := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())
	dynamicClient.PrependReactor("get", "pods", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, metric, nil
	})
	client := &kube.TypedClient{Dynamic: dynamicClient}
	sample, err := samplePodMemoryMetrics(context.Background(), client, "sandbox", "api-shadow")
	if err != nil {
		t.Fatal(err)
	}
	if len(sample.Containers) != 1 || sample.Containers[0].Name != "api" || sample.Containers[0].MemoryBytes != 18*1024*1024 {
		t.Fatalf("unexpected memory sample: %#v", sample)
	}
}

func TestProbeFailureEventsAreBounded(t *testing.T) {
	evidence := &FailureEvidence{}
	events := make([]string, 20)
	for i := range events {
		events[i] = "Readiness probe failed: password=hunter2 " + strings.Repeat("x", 600)
	}
	recordProbeFailureEvents(evidence, events)
	if len(evidence.ProbeFailureEvents) > 8 {
		t.Fatalf("probe event evidence should be capped, got %d", len(evidence.ProbeFailureEvents))
	}
	for _, event := range evidence.ProbeFailureEvents {
		if len(event) > 512 {
			t.Fatalf("probe event should be truncated to 512 bytes, got %d", len(event))
		}
		if strings.Contains(event, "hunter2") {
			t.Fatalf("probe event evidence must redact credentials: %q", event)
		}
	}
}

func TestAppendMemorySampleCapsEvidence(t *testing.T) {
	evidence := &FailureEvidence{}
	for i := 0; i < shadowMemorySampleLimit+3; i++ {
		appendMemorySample(evidence, MemorySample{Timestamp: time.Now(), Containers: []ContainerMemorySample{{Name: "api", Memory: "1Mi"}}})
	}
	if len(evidence.MemoryCurve) != shadowMemorySampleLimit {
		t.Fatalf("want at most %d memory samples, got %d", shadowMemorySampleLimit, len(evidence.MemoryCurve))
	}
}
