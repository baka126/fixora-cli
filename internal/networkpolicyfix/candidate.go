package networkpolicyfix

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"strings"

	"github.com/fixora/kubectl-fixora/internal/kube"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
)

type Plan struct {
	Target           Target                         `json:"target"`
	Workload         WorkloadRef                    `json:"workload"`
	SelectedPodCount int                            `json:"selectedPodCount"`
	Candidate        *networkingv1.NetworkPolicy    `json:"candidate"`
	OriginalSpec     networkingv1.NetworkPolicySpec `json:"originalSpec"`
	ChangedFields    []string                       `json:"changedFields"`
	proof            *candidateProof
}

type candidateProof struct {
	authority               *Authority
	state                   string
	public                  string
	candidate               networkingv1.NetworkPolicySpec
	workloadUID             string
	workloadResourceVersion string
	workloadGeneration      int64
}

func Prepare(ctx context.Context, reader Reader, authority *Authority, target Target) (Plan, error) {
	if reader == nil || authority == nil {
		return Plan{}, errors.New("fresh NetworkPolicy authority and reader required")
	}
	if err := authority.CheckUnchanged(); err != nil {
		return Plan{}, err
	}
	if authority.Target() != target {
		return Plan{}, errors.New("CLI target does not match exact operator authority")
	}
	snapshot, err := Collect(ctx, reader, target.Namespace, target.Name)
	if err != nil {
		return Plan{}, err
	}
	if len(snapshot.Issues) > 0 {
		return Plan{}, errors.New("complete current NetworkPolicy, Pod, namespace and policy evidence required")
	}
	if snapshot.Namespace == nil || snapshot.Namespace.UID == "" || snapshot.Namespace.ResourceVersion == "" {
		return Plan{}, errors.New("target namespace UID and resourceVersion required")
	}
	policy := snapshot.Policy
	if policy == nil || policy.Namespace != target.Namespace || policy.Name != target.Name || policy.UID == "" || policy.ResourceVersion == "" {
		return Plan{}, errors.New("existing NetworkPolicy with stable UID and resourceVersion required")
	}
	count := 0
	for i := range snapshot.Policies {
		p := &snapshot.Policies[i]
		if p.Namespace == target.Namespace && p.Name == target.Name {
			count++
			if p.UID != policy.UID || p.ResourceVersion != policy.ResourceVersion || !reflect.DeepEqual(p.Spec, policy.Spec) {
				return Plan{}, errors.New("NetworkPolicy changed during complete inventory read")
			}
		}
	}
	if count != 1 {
		return Plan{}, errors.New("complete NetworkPolicy inventory must contain the exact target once")
	}
	if objectSourceManaged(policy.Labels, policy.Annotations) {
		return Plan{}, errors.New("source-managed NetworkPolicy requires source review")
	}
	ref := authority.Workload()
	workload, err := reader.GetWorkload(ctx, ref.Kind, target.Namespace, ref.Name)
	if err != nil || workload == nil {
		return Plan{}, errors.New("approved workload could not be read")
	}
	uid, rv, generation, workloadSelector, desired, ready, err := readWorkloadIdentity(workload, ref, target.Namespace)
	if err != nil {
		return Plan{}, err
	}
	if desired <= 0 || ready != desired {
		return Plan{}, errors.New("approved workload must have a complete ready replica set")
	}
	meta := mapAt(workload, "metadata")
	status := mapAt(workload, "status")
	if int64(number(status["observedGeneration"])) < generation {
		return Plan{}, errors.New("approved workload generation is not observed")
	}
	if objectSourceManaged(stringMapAny(meta["labels"]), stringMapAny(meta["annotations"])) {
		return Plan{}, errors.New("source-managed workload requires source review")
	}
	owners := make(map[string]kube.WorkloadOwner)
	workloadPods := make([]corev1.Pod, 0, desired)
	seenUID := map[string]bool{}
	for _, pod := range snapshot.Pods {
		if pod.Namespace != target.Namespace || pod.UID == "" || seenUID[string(pod.UID)] {
			return Plan{}, errors.New("Pod inventory contains incomplete or duplicate identities")
		}
		seenUID[string(pod.UID)] = true
		if !workloadSelector.Matches(labels.Set(pod.Labels)) {
			continue
		}
		if pod.DeletionTimestamp != nil || pod.Status.Phase != corev1.PodRunning || !podReady(pod) {
			return Plan{}, errors.New("approved workload has terminating or unready Pods")
		}
		owner, ownerErr := reader.ResolveOwner(ctx, pod)
		if ownerErr != nil || !owner.Matches(ref.Kind, ref.Name, uid) {
			return Plan{}, errors.New("approved workload Pod ownership is incomplete or ambiguous")
		}
		ownerMeta := mapAt(owner.Object, "metadata")
		if objectSourceManaged(stringMapAny(ownerMeta["labels"]), stringMapAny(ownerMeta["annotations"])) {
			return Plan{}, errors.New("source-managed workload owner requires source review")
		}
		owners[string(pod.UID)] = owner
		workloadPods = append(workloadPods, pod)
	}
	if len(workloadPods) == 0 || len(workloadPods) != desired {
		return Plan{}, errors.New("complete approved workload Pod set is unavailable")
	}
	candidateSpec := authority.NetworkPolicySpec()
	candidateSelector, err := metav1.LabelSelectorAsSelector(&candidateSpec.PodSelector)
	if err != nil {
		return Plan{}, errors.New("approved NetworkPolicy selector is invalid")
	}
	matched := make([]corev1.Pod, 0, len(workloadPods))
	for _, pod := range snapshot.Pods {
		if candidateSelector.Matches(labels.Set(pod.Labels)) {
			matched = append(matched, pod)
		}
	}
	if !samePodUIDSet(matched, workloadPods) {
		return Plan{}, errors.New("approved selector must match exactly the complete intended workload Pod set")
	}
	for _, other := range snapshot.Policies {
		if other.UID == policy.UID {
			continue
		}
		selector, selectorErr := metav1.LabelSelectorAsSelector(&other.Spec.PodSelector)
		if selectorErr != nil {
			return Plan{}, errors.New("another NetworkPolicy has a malformed selector")
		}
		for _, pod := range workloadPods {
			if selector.Matches(labels.Set(pod.Labels)) {
				return Plan{}, errors.New("another NetworkPolicy selects a target Pod; additive effects require review")
			}
		}
	}
	if err := authority.CheckUnchanged(); err != nil {
		return Plan{}, err
	}
	candidate := policy.DeepCopy()
	candidate.Spec = candidateSpec
	plan := Plan{Target: target, Workload: ref, SelectedPodCount: len(workloadPods), Candidate: candidate, OriginalSpec: *policy.Spec.DeepCopy(), ChangedFields: []string{"/spec"}}
	proof := &candidateProof{authority: authority, state: snapshotDigest(snapshot, workload, owners, workloadPods), candidate: *candidateSpec.DeepCopy(), workloadUID: uid, workloadResourceVersion: rv, workloadGeneration: generation}
	plan.proof = proof
	proof.public = planDigest(plan)
	return plan, nil
}

func readWorkloadIdentity(obj map[string]any, ref WorkloadRef, namespace string) (uid, rv string, generation int64, selector labels.Selector, desired, ready int, err error) {
	if obj["kind"] != ref.Kind || obj["apiVersion"] != "apps/v1" {
		err = errors.New("approved workload kind or API identity mismatch")
		return
	}
	meta := mapAt(obj, "metadata")
	if meta["name"] != ref.Name || meta["namespace"] != namespace {
		err = errors.New("approved workload identity mismatch")
		return
	}
	uid, _ = meta["uid"].(string)
	rv, _ = meta["resourceVersion"].(string)
	generation = int64(number(meta["generation"]))
	if uid == "" || rv == "" || generation <= 0 {
		err = errors.New("approved workload UID, resourceVersion and generation required")
		return
	}
	var ls metav1.LabelSelector
	raw, e := json.Marshal(mapAt(mapAt(obj, "spec"), "selector"))
	if e != nil || json.Unmarshal(raw, &ls) != nil || !selectorNonEmpty(ls) {
		err = errors.New("approved workload selector unavailable")
		return
	}
	selector, err = metav1.LabelSelectorAsSelector(&ls)
	if err != nil {
		err = errors.New("approved workload selector invalid")
		return
	}
	spec, status := mapAt(obj, "spec"), mapAt(obj, "status")
	switch ref.Kind {
	case "Deployment", "StatefulSet":
		desired, ready = int(number(spec["replicas"])), int(number(status["readyReplicas"]))
	case "DaemonSet":
		desired, ready = int(number(status["desiredNumberScheduled"])), int(number(status["numberReady"]))
	}
	if desired <= 0 || ready != desired {
		err = errors.New("approved workload must have complete ready replicas")
		return
	}
	switch ref.Kind {
	case "Deployment":
		if int(number(status["updatedReplicas"])) != desired || int(number(status["availableReplicas"])) != desired || int(number(status["replicas"])) != desired {
			err = errors.New("Deployment rollout is not stable")
		}
	case "StatefulSet":
		if int(number(status["updatedReplicas"])) != desired || status["currentRevision"] == "" || status["currentRevision"] != status["updateRevision"] {
			err = errors.New("StatefulSet revision is not stable")
		}
	case "DaemonSet":
		if int(number(status["updatedNumberScheduled"])) != desired || int(number(status["numberUnavailable"])) != 0 || int(number(status["numberMisscheduled"])) != 0 {
			err = errors.New("DaemonSet rollout is not stable")
		}
	}
	return
}

func podReady(pod corev1.Pod) bool {
	for _, condition := range pod.Status.Conditions {
		if condition.Type == corev1.PodReady {
			return condition.Status == corev1.ConditionTrue
		}
	}
	return false
}
func samePodUIDSet(a, b []corev1.Pod) bool {
	if len(a) != len(b) {
		return false
	}
	set := map[string]bool{}
	for _, pod := range a {
		if pod.UID == "" || set[string(pod.UID)] {
			return false
		}
		set[string(pod.UID)] = true
	}
	for _, pod := range b {
		if !set[string(pod.UID)] {
			return false
		}
	}
	return true
}
func objectSourceManaged(ls, as map[string]string) bool {
	for key, value := range ls {
		lower, val := strings.ToLower(key), strings.ToLower(value)
		if val != "" && (lower == "app.kubernetes.io/managed-by" && val == "helm" || strings.Contains(lower, "helm") || strings.Contains(lower, "argocd.argoproj.io") || strings.Contains(lower, "toolkit.fluxcd.io")) {
			return true
		}
	}
	for key, value := range as {
		lower := strings.ToLower(key)
		if value != "" && (strings.Contains(lower, "helm") || strings.Contains(lower, "argocd.argoproj.io") || strings.Contains(lower, "toolkit.fluxcd.io")) {
			return true
		}
	}
	return false
}
func mapAt(obj map[string]any, key string) map[string]any {
	if out, ok := obj[key].(map[string]any); ok {
		return out
	}
	return map[string]any{}
}
func stringMapAny(value any) map[string]string {
	out := map[string]string{}
	switch raw := value.(type) {
	case map[string]string:
		for k, v := range raw {
			out[k] = v
		}
	case map[string]any:
		for k, v := range raw {
			if s, ok := v.(string); ok {
				out[k] = s
			}
		}
	}
	return out
}
func number(value any) float64 {
	switch n := value.(type) {
	case int:
		return float64(n)
	case int32:
		return float64(n)
	case int64:
		return float64(n)
	case float64:
		return n
	case json.Number:
		v, _ := n.Float64()
		return v
	}
	return 0
}
func cloneMap(in map[string]any) map[string]any {
	raw, _ := json.Marshal(in)
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return out
}
func snapshotDigest(s Snapshot, w map[string]any, owners map[string]kube.WorkloadOwner, pods []corev1.Pod) string {
	raw, _ := json.Marshal([]any{s.Policy, s.Policies, s.Namespace, s.Pods, w, owners, pods})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func planDigest(p Plan) string {
	raw, _ := json.Marshal([]any{p.Target, p.Workload, p.SelectedPodCount, p.Candidate, p.OriginalSpec, p.ChangedFields})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func (p Plan) CheckBound() error {
	if p.proof == nil || p.proof.authority == nil || p.Candidate == nil || p.proof.public == "" || p.proof.public != planDigest(p) {
		return errors.New("NetworkPolicy plan was changed or lacks private proof")
	}
	if err := p.proof.authority.CheckUnchanged(); err != nil {
		return err
	}
	if !reflect.DeepEqual(p.Candidate.Spec, p.proof.candidate) || p.Candidate.UID == "" {
		return errors.New("NetworkPolicy candidate no longer matches approved authority")
	}
	return nil
}
