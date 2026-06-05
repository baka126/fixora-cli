package shadow

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/strategicpatch"
	"k8s.io/apimachinery/pkg/util/yaml"

	"github.com/fixora/kubectl-fixora/internal/kube"
)

const labelSandbox = "fixora.io/sandbox"
const labelOriginal = "fixora.io/original-pod"
const labelSession = "fixora.io/session"
const annotationExpires = "fixora.io/expires-at"

type clonePlan struct {
	Original       *corev1.Pod
	UnpatchedClone *corev1.Pod
	Clone          *corev1.Pod
	Policy         *networkingv1.NetworkPolicy
	Warnings       []string
}

func buildClonePlan(ctx context.Context, c *kube.TypedClient, req Request, session string) (clonePlan, error) {
	original, err := podTemplateForResource(ctx, c, req.Namespace, req.Resource, req.Finding.PodName)
	if err != nil {
		return clonePlan{}, err
	}
	clone := sanitizePod(original, session, req.Timeout)
	clone.Name = cloneName(original.Name, session)
	unpatchedClone := clone.DeepCopy()
	if err := applyPatchToPod(clone, req.Patch); err != nil {
		return clonePlan{}, err
	}
	policy := sandboxNetworkPolicy(clone.Namespace, clone.Name+"-netpol", session, req.Egress)
	return clonePlan{Original: original, UnpatchedClone: unpatchedClone, Clone: clone, Policy: policy, Warnings: cloneWarnings(req.Egress)}, nil
}

func podTemplateForResource(ctx context.Context, c *kube.TypedClient, namespace, resource, podHint string) (*corev1.Pod, error) {
	kind, name := splitResource(resource)
	if namespace == "" {
		namespace = "default"
	}
	switch strings.ToLower(kind) {
	case "pod", "pods":
		return c.GetTypedPod(ctx, namespace, name)
	case "deployment", "deploy", "deployments":
		obj, err := c.Clientset.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return nil, err
		}
		return podFromTemplate(namespace, name, "Deployment", obj.Spec.Template), nil
	case "statefulset", "statefulsets", "sts":
		obj, err := c.Clientset.AppsV1().StatefulSets(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return nil, err
		}
		return podFromTemplate(namespace, name, "StatefulSet", obj.Spec.Template), nil
	case "daemonset", "daemonsets", "ds":
		obj, err := c.Clientset.AppsV1().DaemonSets(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return nil, err
		}
		return podFromTemplate(namespace, name, "DaemonSet", obj.Spec.Template), nil
	case "replicaset", "replicasets", "rs":
		obj, err := c.Clientset.AppsV1().ReplicaSets(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return nil, err
		}
		return podFromTemplate(namespace, name, "ReplicaSet", obj.Spec.Template), nil
	case "job", "jobs":
		obj, err := c.Clientset.BatchV1().Jobs(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return nil, err
		}
		return podFromTemplate(namespace, name, "Job", obj.Spec.Template), nil
	case "cronjob", "cronjobs", "cj":
		obj, err := c.Clientset.BatchV1().CronJobs(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return nil, err
		}
		return podFromTemplate(namespace, name, "CronJob", obj.Spec.JobTemplate.Spec.Template), nil
	}
	if podHint != "" {
		return c.GetTypedPod(ctx, namespace, podHint)
	}
	return nil, fmt.Errorf("shadow verification supports Pod, Deployment, StatefulSet, DaemonSet, ReplicaSet, Job, and CronJob; got %q", kind)
}

func podFromTemplate(namespace, ownerName, ownerKind string, tpl corev1.PodTemplateSpec) *corev1.Pod {
	pod := &corev1.Pod{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Pod"},
		ObjectMeta: metav1.ObjectMeta{
			Name:        ownerName,
			Namespace:   namespace,
			Labels:      copyStringMap(tpl.Labels),
			Annotations: copyStringMap(tpl.Annotations),
		},
		Spec: *tpl.Spec.DeepCopy(),
	}
	if pod.Spec.RestartPolicy == "" && ownerKind != "Job" && ownerKind != "CronJob" {
		pod.Spec.RestartPolicy = corev1.RestartPolicyAlways
	}
	return pod
}

func sanitizePod(original *corev1.Pod, session string, timeout time.Duration) *corev1.Pod {
	clone := original.DeepCopy()
	expires := time.Now().UTC().Add(timeout)
	if timeout <= 0 {
		expires = time.Now().UTC().Add(10 * time.Minute)
	}
	clone.TypeMeta = metav1.TypeMeta{APIVersion: "v1", Kind: "Pod"}
	clone.ResourceVersion = ""
	clone.UID = ""
	clone.ManagedFields = nil
	clone.Generation = 0
	clone.CreationTimestamp = metav1.Time{}
	clone.DeletionTimestamp = nil
	clone.DeletionGracePeriodSeconds = nil
	clone.OwnerReferences = nil
	clone.Finalizers = nil
	clone.Status = corev1.PodStatus{}
	clone.Labels = map[string]string{
		labelSandbox:  "true",
		labelOriginal: safeLabelValue(original.Name),
		labelSession:  session,
	}
	clone.Annotations = map[string]string{
		annotationExpires: expires.Format(time.RFC3339),
	}
	clone.GenerateName = ""
	clone.Spec.NodeName = ""
	for i, v := range clone.Spec.Volumes {
		if v.PersistentVolumeClaim != nil {
			clone.Spec.Volumes[i].PersistentVolumeClaim = nil
			clone.Spec.Volumes[i].EmptyDir = &corev1.EmptyDirVolumeSource{}
		}
	}
	clone.Spec.NodeSelector = nil
	clone.Spec.Affinity = nil
	return clone
}

func applyPatchToPod(pod *corev1.Pod, patchYAML string) error {
	patchYAML = strings.TrimSpace(patchYAML)
	if patchYAML == "" {
		return fmt.Errorf("empty patch")
	}
	if hasMultipleYAMLDocuments(patchYAML) {
		return fmt.Errorf("multi-document YAML patches are not supported for shadow verification")
	}
	patchJSON, err := yaml.ToJSON([]byte(patchYAML))
	if err != nil {
		return fmt.Errorf("parse patch yaml: %w", err)
	}
	var obj map[string]any
	if err := json.Unmarshal(patchJSON, &obj); err != nil {
		return fmt.Errorf("decode patch yaml: %w", err)
	}
	podPatch, err := podStrategicPatch(obj)
	if err != nil {
		return err
	}
	base, err := json.Marshal(pod)
	if err != nil {
		return err
	}
	merged, err := strategicpatch.StrategicMergePatch(base, podPatch, corev1.Pod{})
	if err != nil {
		return fmt.Errorf("merge patch into shadow pod: %w", err)
	}
	return json.Unmarshal(merged, pod)
}

func podStrategicPatch(obj map[string]any) ([]byte, error) {
	kind := strings.ToLower(fmt.Sprint(obj["kind"]))
	if kind == "pod" || kind == "" {
		return json.Marshal(removeIdentity(obj))
	}
	spec, ok := nestedMap(obj, "spec")
	if !ok {
		return nil, fmt.Errorf("patch does not include a spec")
	}
	template, ok := nestedMap(spec, "template")
	if ok {
		templateSpec, ok := nestedMap(template, "spec")
		if !ok {
			return nil, fmt.Errorf("workload patch does not include spec.template.spec")
		}
		return json.Marshal(map[string]any{"spec": templateSpec})
	}
	if _, ok := spec["containers"]; ok {
		return json.Marshal(map[string]any{"spec": spec})
	}
	return nil, fmt.Errorf("cannot project %s patch into a shadow pod; only pod template spec changes are supported", kind)
}

func sandboxNetworkPolicy(namespace, name, session, egress string) *networkingv1.NetworkPolicy {
	policyTypes := []networkingv1.PolicyType{networkingv1.PolicyTypeIngress, networkingv1.PolicyTypeEgress}
	np := &networkingv1.NetworkPolicy{
		TypeMeta: metav1.TypeMeta{APIVersion: "networking.k8s.io/v1", Kind: "NetworkPolicy"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels: map[string]string{
				labelSandbox: "true",
				labelSession: session,
			},
		},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{labelSession: session}},
			PolicyTypes: policyTypes,
			Ingress:     []networkingv1.NetworkPolicyIngressRule{},
			Egress:      []networkingv1.NetworkPolicyEgressRule{},
		},
	}
	if strings.EqualFold(egress, "allow") {
		np.Spec.Egress = []networkingv1.NetworkPolicyEgressRule{{}}
	}
	return np
}

func cloneWarnings(egress string) []string {
	warnings := []string{"shadow pod labels are stripped so existing Services should not route traffic to the clone"}
	if strings.EqualFold(egress, "allow") || egress == "" {
		warnings = append(warnings, "shadow NetworkPolicy blocks ingress but allows egress for parity; downstream systems may still see clone traffic")
	} else {
		warnings = append(warnings, "shadow egress is blocked; readiness may fail for workloads that need external dependencies")
	}
	return warnings
}

func splitResource(resource string) (string, string) {
	parts := strings.SplitN(resource, "/", 2)
	if len(parts) != 2 {
		return resource, ""
	}
	return parts[0], parts[1]
}

func cloneName(original, session string) string {
	base := strings.ToLower(original)
	base = strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' {
			return r
		}
		return '-'
	}, base)
	if len(base) > 40 {
		base = base[:40]
	}
	base = strings.Trim(base, "-")
	if base == "" {
		base = "shadow"
	}
	return base + "-fixora-" + session[:8]
}

func safeLabelValue(value string) string {
	value = strings.ToLower(value)
	value = strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '.' || r == '_' {
			return r
		}
		return '-'
	}, value)
	if len(value) > 63 {
		value = value[:63]
	}
	return strings.Trim(value, "-_.")
}

func copyStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func hasMultipleYAMLDocuments(value string) bool {
	docs := 0
	for _, doc := range strings.Split(value, "\n---") {
		if strings.TrimSpace(doc) != "" {
			docs++
			if docs > 1 {
				return true
			}
		}
	}
	return false
}

func removeIdentity(obj map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range obj {
		if k == "apiVersion" || k == "kind" || k == "status" {
			continue
		}
		if k == "metadata" {
			meta, ok := v.(map[string]any)
			if !ok {
				continue
			}
			next := map[string]any{}
			for mk, mv := range meta {
				if mk == "name" || mk == "namespace" || mk == "labels" || mk == "annotations" || mk == "ownerReferences" || mk == "uid" || mk == "resourceVersion" || mk == "managedFields" {
					continue
				}
				next[mk] = mv
			}
			if len(next) > 0 {
				out[k] = next
			}
			continue
		}
		out[k] = v
	}
	return out
}

func nestedMap(obj map[string]any, key string) (map[string]any, bool) {
	value, ok := obj[key]
	if !ok {
		return nil, false
	}
	m, ok := value.(map[string]any)
	return m, ok
}
