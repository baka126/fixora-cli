package shadow

import (
	"context"
	"encoding/json"
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
