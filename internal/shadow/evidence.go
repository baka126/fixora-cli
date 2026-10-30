package shadow

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/fixora/kubectl-fixora/internal/kube"
	"github.com/fixora/kubectl-fixora/internal/redact"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

const (
	shadowMemorySampleInterval = 15 * time.Second
	shadowMemorySampleLimit    = 32
	shadowProbeEventLimit      = 8
	shadowProbeEventByteLimit  = 512
)

func collectFailureEvidence(ctx context.Context, c *kube.TypedClient, sourceNamespace string, source, candidate *corev1.Pod) *FailureEvidence {
	evidence := &FailureEvidence{}
	if source != nil {
		evidence.Probes = probeEvidence(source)
		evidence.ConfigMaps = configMapReferenceEvidence(ctx, c, sourceNamespace, source)
	}
	if candidate != nil {
		updateCandidateEvidence(evidence, candidate, time.Now())
	}
	return evidence
}

func configMapReferenceEvidence(ctx context.Context, c *kube.TypedClient, namespace string, pod *corev1.Pod) []ConfigMapReferenceEvidence {
	if pod == nil {
		return nil
	}
	type reference struct {
		container string
		name      string
		key       string
		optional  bool
	}
	var references []reference
	add := func(container, name, key string, optional bool) {
		if strings.TrimSpace(name) != "" {
			references = append(references, reference{container: container, name: name, key: key, optional: optional})
		}
	}
	containers := append(append([]corev1.Container{}, pod.Spec.InitContainers...), pod.Spec.Containers...)
	for _, container := range containers {
		for _, source := range container.EnvFrom {
			if source.ConfigMapRef != nil {
				add(container.Name, source.ConfigMapRef.Name, "*", source.ConfigMapRef.Optional != nil && *source.ConfigMapRef.Optional)
			}
		}
		for _, env := range container.Env {
			if env.ValueFrom != nil && env.ValueFrom.ConfigMapKeyRef != nil {
				ref := env.ValueFrom.ConfigMapKeyRef
				add(container.Name, ref.Name, ref.Key, ref.Optional != nil && *ref.Optional)
			}
		}
	}
	addConfigMap := func(volumeName, name string, items []corev1.KeyToPath, optional *bool) {
		if strings.TrimSpace(name) == "" {
			return
		}
		isOptional := optional != nil && *optional
		if len(items) == 0 {
			add(volumeName, name, "*", isOptional)
			return
		}
		for _, item := range items {
			add(volumeName, name, item.Key, isOptional)
		}
	}
	for _, volume := range pod.Spec.Volumes {
		if source := volume.ConfigMap; source != nil {
			addConfigMap(volume.Name, source.Name, source.Items, source.Optional)
		}
		if volume.Projected != nil {
			for _, projected := range volume.Projected.Sources {
				if source := projected.ConfigMap; source != nil {
					addConfigMap(volume.Name, source.Name, source.Items, source.Optional)
				}
			}
		}
	}

	type configMapRead struct {
		found bool
		keys  map[string]bool
		err   error
	}
	cache := map[string]configMapRead{}
	seen := map[string]bool{}
	result := make([]ConfigMapReferenceEvidence, 0, len(references))
	for _, ref := range references {
		identity := ref.container + "\x00" + ref.name + "\x00" + ref.key
		if seen[identity] {
			continue
		}
		seen[identity] = true
		read, cached := cache[ref.name]
		if !cached {
			read.keys = map[string]bool{}
			if c == nil || c.Clientset == nil {
				read.err = fmt.Errorf("typed Kubernetes client unavailable")
			} else {
				configMap, err := c.Clientset.CoreV1().ConfigMaps(namespace).Get(ctx, ref.name, metav1.GetOptions{})
				switch {
				case apierrors.IsNotFound(err):
					read.found = false
				case err != nil:
					read.err = err
				default:
					read.found = true
					for key := range configMap.Data {
						read.keys[key] = true
					}
					for key := range configMap.BinaryData {
						read.keys[key] = true
					}
				}
			}
			cache[ref.name] = read
		}
		status := "object-present"
		switch {
		case read.err != nil:
			status = "unknown"
		case !read.found:
			status = "not-found"
		case ref.key != "*" && !read.keys[ref.key]:
			status = "key-missing"
		case ref.key != "*":
			status = "key-present"
		}
		result = append(result, ConfigMapReferenceEvidence{
			Container: ref.container, Name: ref.name, Key: ref.key,
			SourceStatus: status, SandboxStatus: "omitted-by-isolation", Optional: ref.optional,
		})
	}
	return result
}

func probeEvidence(pod *corev1.Pod) []ProbeEvidence {
	if pod == nil {
		return nil
	}
	var result []ProbeEvidence
	containers := append(append([]corev1.Container{}, pod.Spec.InitContainers...), pod.Spec.Containers...)
	for _, container := range containers {
		appendProbe := func(kind string, probe *corev1.Probe) {
			if probe == nil {
				return
			}
			evidence := ProbeEvidence{Container: container.Name, Type: kind}
			switch {
			case probe.HTTPGet != nil:
				evidence.Protocol = "http"
				evidence.Path = probe.HTTPGet.Path
				evidence.Port = probe.HTTPGet.Port.String()
				evidence.Scheme = string(probe.HTTPGet.Scheme)
			case probe.TCPSocket != nil:
				evidence.Protocol = "tcp"
				evidence.Port = probe.TCPSocket.Port.String()
			case probe.GRPC != nil:
				evidence.Protocol = "grpc"
				evidence.Port = fmt.Sprint(probe.GRPC.Port)
			}
			result = append(result, evidence)
		}
		appendProbe("readiness", container.ReadinessProbe)
		appendProbe("liveness", container.LivenessProbe)
		appendProbe("startup", container.StartupProbe)
	}
	return result
}

func updateCandidateEvidence(evidence *FailureEvidence, pod *corev1.Pod, observedAt time.Time) {
	if evidence == nil || pod == nil {
		return
	}
	evidence.Probes = probeEvidence(pod)
	statuses := map[string]corev1.ContainerStatus{}
	for _, status := range append(append([]corev1.ContainerStatus{}, pod.Status.InitContainerStatuses...), pod.Status.ContainerStatuses...) {
		statuses[status.Name] = status
	}
	containers := append(append([]corev1.Container{}, pod.Spec.InitContainers...), pod.Spec.Containers...)
	evidence.Containers = make([]ContainerEvidence, 0, len(containers))
	for _, container := range containers {
		item := ContainerEvidence{Name: container.Name, RequestedImage: container.Image}
		if quantity, ok := container.Resources.Requests[corev1.ResourceMemory]; ok {
			item.MemoryRequest = quantity.String()
		}
		if quantity, ok := container.Resources.Limits[corev1.ResourceMemory]; ok {
			item.MemoryLimit = quantity.String()
		}
		if status, ok := statuses[container.Name]; ok {
			item.RuntimeImage = status.Image
			item.ImageID = status.ImageID
			item.Restarts = status.RestartCount
			switch {
			case status.State.Terminated != nil:
				item.TerminationReason = status.State.Terminated.Reason
				code := status.State.Terminated.ExitCode
				item.ExitCode = &code
			case status.State.Waiting != nil:
				item.TerminationReason = status.State.Waiting.Reason
			}
			if status.LastTerminationState.Terminated != nil && item.TerminationReason == "" {
				item.TerminationReason = status.LastTerminationState.Terminated.Reason
				code := status.LastTerminationState.Terminated.ExitCode
				item.ExitCode = &code
			}
		}
		evidence.Containers = append(evidence.Containers, item)
	}
	if evidence.ReadyAt == nil {
		for _, condition := range pod.Status.Conditions {
			if condition.Type != corev1.PodReady || condition.Status != corev1.ConditionTrue {
				continue
			}
			readyAt := observedAt
			if !condition.LastTransitionTime.IsZero() {
				readyAt = condition.LastTransitionTime.Time
			} else {
				evidence.ReadyTimingEstimated = true
			}
			evidence.ReadyAt = &readyAt
			if !pod.CreationTimestamp.IsZero() && !readyAt.Before(pod.CreationTimestamp.Time) {
				seconds := int64(readyAt.Sub(pod.CreationTimestamp.Time).Seconds())
				evidence.TimeToReadySeconds = &seconds
			}
			break
		}
	}
}

func samplePodMemoryMetrics(ctx context.Context, c *kube.TypedClient, namespace, podName string) (MemorySample, error) {
	if c == nil || c.Dynamic == nil {
		return MemorySample{}, fmt.Errorf("metrics API client unavailable")
	}
	resourceClient := c.Dynamic.Resource(schema.GroupVersionResource{Group: "metrics.k8s.io", Version: "v1beta1", Resource: "pods"}).Namespace(namespace)
	object, err := resourceClient.Get(ctx, podName, metav1.GetOptions{})
	if err != nil {
		return MemorySample{}, err
	}
	containers, found, err := unstructured.NestedSlice(object.Object, "containers")
	if err != nil || !found {
		return MemorySample{}, fmt.Errorf("pod metrics response has no container samples")
	}
	sample := MemorySample{}
	if timestamp, _, err := unstructured.NestedString(object.Object, "timestamp"); err == nil && timestamp != "" {
		parsed, parseErr := time.Parse(time.RFC3339Nano, timestamp)
		if parseErr == nil {
			sample.Timestamp = parsed
		}
	}
	if sample.Timestamp.IsZero() {
		sample.Timestamp = time.Now()
	}
	for _, raw := range containers {
		container, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		name, _, _ := unstructured.NestedString(container, "name")
		memory, found, _ := unstructured.NestedString(container, "usage", "memory")
		if !found || memory == "" {
			continue
		}
		quantity, err := resource.ParseQuantity(memory)
		if err != nil {
			continue
		}
		sample.Containers = append(sample.Containers, ContainerMemorySample{Name: name, Memory: quantity.String(), MemoryBytes: quantity.Value()})
	}
	if len(sample.Containers) == 0 {
		return MemorySample{}, fmt.Errorf("pod metrics response has no memory quantities")
	}
	sort.Slice(sample.Containers, func(i, j int) bool { return sample.Containers[i].Name < sample.Containers[j].Name })
	return sample, nil
}

func appendMemorySample(evidence *FailureEvidence, sample MemorySample) {
	if evidence == nil || len(sample.Containers) == 0 || len(evidence.MemoryCurve) >= shadowMemorySampleLimit {
		return
	}
	evidence.MemoryCurve = append(evidence.MemoryCurve, sample)
	evidence.MemoryMetricsStatus = "available"
}

func sampleMemoryEvidence(ctx context.Context, c *kube.TypedClient, namespace, podName string, evidence *FailureEvidence) {
	if evidence == nil {
		return
	}
	sampleCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	sample, err := samplePodMemoryMetrics(sampleCtx, c, namespace, podName)
	if err != nil {
		if len(evidence.MemoryCurve) == 0 {
			evidence.MemoryMetricsStatus = "unavailable"
		}
		return
	}
	appendMemorySample(evidence, sample)
}

func recordProbeFailureEvents(evidence *FailureEvidence, events []string) {
	if evidence == nil {
		return
	}
	for _, event := range events {
		folded := strings.ToLower(event)
		if strings.Contains(folded, "probe failed") || strings.Contains(folded, "readiness probe") || strings.Contains(folded, "liveness probe") || strings.Contains(folded, "startup probe") {
			if len(evidence.ProbeFailureEvents) >= shadowProbeEventLimit {
				return
			}
			event = redact.KubernetesText(event)
			if len(event) > shadowProbeEventByteLimit {
				event = event[:shadowProbeEventByteLimit]
			}
			evidence.ProbeFailureEvents = appendUnique(evidence.ProbeFailureEvents, event)
		}
	}
}
