package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/fixora/kubectl-fixora/internal/image"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/yaml"
	"reflect"
	"strings"
	"time"

	"github.com/fixora/kubectl-fixora/internal/analyzer"
	"github.com/fixora/kubectl-fixora/internal/fix"
	"github.com/fixora/kubectl-fixora/internal/kube"
)

type TargetReader interface {
	GetResource(context.Context, string, string) (map[string]any, error)
	GetPod(context.Context, string, string) (kube.Pod, error)
}

// PreflightController validates delivery semantics against the current object.
// Nonzero StatefulSet partitions conservatively require source/manual review.
type TargetSnapshot struct{ UID, ResourceVersion string }

func PreflightController(ctx context.Context, r TargetReader, f analyzer.Finding, p fix.Plan) (string, error) {
	snapshot, err := PreflightControllerSnapshot(ctx, r, f, p)
	return snapshot.UID, err
}

func PreflightControllerSnapshot(ctx context.Context, r TargetReader, f analyzer.Finding, p fix.Plan) (TargetSnapshot, error) {
	p = fix.WithTargetPolicy(p)
	if !p.ApplyEligible {
		return TargetSnapshot{}, fmt.Errorf("repair is review-only: %s", strings.Join(p.BlockedReasons, "; "))
	}
	obj, err := r.GetResource(ctx, f.Namespace, strings.ToLower(f.ResourceKind)+"/"+f.ResourceName)
	if err != nil {
		return TargetSnapshot{}, err
	}
	meta, _ := obj["metadata"].(map[string]any)
	uid, _ := meta["uid"].(string)
	if meta["name"] != f.ResourceName || meta["namespace"] != f.Namespace || obj["kind"] != f.ResourceKind || obj["apiVersion"] != kube.WorkloadVersion(f.ResourceKind) || uid == "" {
		return TargetSnapshot{}, fmt.Errorf("live target identity could not be verified")
	}
	if p.TargetUID != "" && uid != p.TargetUID {
		return TargetSnapshot{}, fmt.Errorf("repair target was replaced since diagnosis; collect fresh evidence")
	}
	spec, _ := obj["spec"].(map[string]any)
	switch f.ResourceKind {
	case "StatefulSet", "DaemonSet":
		strategy, _ := spec["updateStrategy"].(map[string]any)
		if typ, _ := strategy["type"].(string); typ != "" && typ != "RollingUpdate" {
			return TargetSnapshot{}, fmt.Errorf("%s uses %s; update existing Pods through the source/manual procedure", f.ResourceKind, typ)
		}
		rolling, _ := strategy["rollingUpdate"].(map[string]any)
		if f.ResourceKind == "StatefulSet" && number(rolling["partition"]) > 0 {
			return TargetSnapshot{}, fmt.Errorf("StatefulSet partition excludes existing replicas; source/manual review required")
		}
	}
	if f.PodName != "" {
		pod, err := r.GetPod(ctx, f.Namespace, f.PodName)
		if err != nil {
			return TargetSnapshot{}, fmt.Errorf("recheck failing Pod: %w", err)
		}
		if p.TargetPodUID != "" && pod.Metadata.UID != p.TargetPodUID {
			return TargetSnapshot{}, fmt.Errorf("failing Pod was replaced since diagnosis; collect fresh evidence")
		}
		reader, ok := r.(kube.Reader)
		if !ok {
			return TargetSnapshot{}, fmt.Errorf("ownership reader unavailable")
		}
		owner, err := kube.ResolveWorkloadOwner(ctx, reader, pod)
		if err != nil || owner.Kind != f.ResourceKind || owner.Name != f.ResourceName || owner.UID != uid {
			return TargetSnapshot{}, fmt.Errorf("failing Pod ownership no longer matches the repair target")
		}
		template, ok := kube.WorkloadPodSpec(obj)
		if !ok {
			return TargetSnapshot{}, fmt.Errorf("live Pod template unavailable")
		}
		var desired kube.PodSpec
		data, _ := json.Marshal(template)
		if json.Unmarshal(data, &desired) != nil {
			return TargetSnapshot{}, fmt.Errorf("invalid Pod template")
		}
		if !sameContainerConfig(pod.Spec.Containers, desired.Containers) || !sameContainerConfig(pod.Spec.InitContainers, desired.InitContainers) || !reflect.DeepEqual(pod.Spec.SecurityContext, desired.SecurityContext) || !reflect.DeepEqual(withoutServiceAccount(pod.Spec.Volumes, "name"), withoutServiceAccount(desired.Volumes, "name")) {
			return TargetSnapshot{}, fmt.Errorf("failing Pod configuration differs from the current template; collect fresh evidence")
		}
	}
	version, _ := meta["resourceVersion"].(string)
	return TargetSnapshot{UID: uid, ResourceVersion: version}, nil
}

func sameContainerConfig(observed, template []kube.Container) bool {
	if len(observed) != len(template) {
		return false
	}
	for i := range observed {
		a, b := observed[i], template[i]
		// Admission/defaulted probe timings and service-account mounts do not change
		// startup intent. Compare the fields that underpin inferred root causes.
		if a.Name != b.Name || a.Image != b.Image || !reflect.DeepEqual(a.Command, b.Command) || !reflect.DeepEqual(a.Args, b.Args) || !reflect.DeepEqual(a.Env, b.Env) || !reflect.DeepEqual(a.EnvFrom, b.EnvFrom) || !reflect.DeepEqual(a.Resources, b.Resources) || !reflect.DeepEqual(a.SecurityContext, b.SecurityContext) || !reflect.DeepEqual(a.ReadinessProbe, b.ReadinessProbe) || !reflect.DeepEqual(a.LivenessProbe, b.LivenessProbe) || !reflect.DeepEqual(a.StartupProbe, b.StartupProbe) || !reflect.DeepEqual(a.Ports, b.Ports) || !reflect.DeepEqual(withoutServiceAccount(a.VolumeMounts, "mountPath"), withoutServiceAccount(b.VolumeMounts, "mountPath")) {
			return false
		}
	}
	return true
}
func number(v any) int64 {
	switch n := v.(type) {
	case float64:
		return int64(n)
	case int:
		return int64(n)
	case int64:
		return n
	}
	return 0
}

// VerifyPod checks the same live Pod after an image-only apply. A shadow pass
// does not prove that the kubelet has started the new image in production.
func VerifyPod(ctx context.Context, r TargetReader, f analyzer.Finding, p fix.Plan, timeout time.Duration) RolloutOutcome {
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var patch map[string]any
	data, parseErr := yaml.ToJSON([]byte(p.PatchTemplate))
	if parseErr != nil || json.Unmarshal(data, &patch) != nil {
		return RolloutOutcome{Class: RolloutInvalid, Summary: "Pod image proposal is invalid"}
	}
	desired, ok := kube.WorkloadPodSpec(patch)
	if !ok || desired["containers"] == nil && desired["initContainers"] == nil {
		return RolloutOutcome{Class: RolloutInvalid, Summary: "Pod image proposal has no concrete container change"}
	}
	for {
		pod, err := r.GetPod(ctx, f.Namespace, f.ResourceName)
		if err != nil {
			return RolloutOutcome{Class: RolloutUnknown, Summary: "could not verify live Pod: " + err.Error()}
		}
		if pod.Metadata.UID != p.TargetUID || pod.Metadata.Namespace != f.Namespace || pod.Metadata.Name != f.ResourceName {
			return RolloutOutcome{Class: RolloutInvalid, Summary: "Pod was replaced during verification; repair outcome is unverified"}
		}
		ready := pod.Status.Phase == "Running" && len(pod.Status.ContainerStatuses) > 0 && podReady(pod)
		for _, cs := range pod.Status.ContainerStatuses {
			if !cs.Ready || cs.State["running"].StartedAt == "" {
				ready = false
			}
		}
		// Container spec alone changes before the kubelet starts the image. Require
		// reported status image to agree too; missing status is not proof of success.
		for _, section := range []string{"containers", "initContainers"} {
			containers, _ := desired[section].([]any)
			for _, raw := range containers {
				c, _ := raw.(map[string]any)
				found := false
				statuses := pod.Status.ContainerStatuses
				if section == "initContainers" {
					statuses = pod.Status.InitStatuses
				}
				for _, cs := range statuses {
					if cs.Name == c["name"] && runtimeImageMatches(fmt.Sprint(c["image"]), cs) && podSpecHasImage(pod, section, fmt.Sprint(c["name"]), fmt.Sprint(c["image"])) {
						found = true
					}
				}
				if !found {
					ready = false
				}
			}
		}
		if ready {
			return RolloutOutcome{Class: RolloutHealthy, Summary: "Pod/" + f.ResourceName + " is Ready with the proposed image"}
		}
		select {
		case <-ctx.Done():
			return RolloutOutcome{Class: RolloutTimeout, Summary: "Pod did not become Ready with the proposed image before the timeout"}
		case <-time.After(time.Second):
		}
	}
}

func withoutServiceAccount(items []map[string]any, key string) []map[string]any {
	out := []map[string]any{}
	for _, item := range items {
		s, _ := item[key].(string)
		if key == "mountPath" && s == "/var/run/secrets/kubernetes.io/serviceaccount" {
			continue
		}
		if key == "name" && strings.HasPrefix(s, "kube-api-access-") && projectedServiceAccount(item) {
			continue
		}
		out = append(out, item)
	}
	return out
}
func podReady(pod kube.Pod) bool {
	ready := false
	conditions := map[string]string{}
	for _, c := range pod.Status.Conditions {
		conditions[c.Type] = c.Status
		if c.Type == "Ready" && c.Status == "True" {
			ready = true
		}
	}
	for _, g := range pod.Spec.ReadinessGates {
		if conditions[g.ConditionType] != "True" {
			return false
		}
	}
	return ready
}
func runtimeImageMatches(requested string, cs kube.ContainerStatus) bool {
	if cs.ImageID == "" {
		return false
	}
	canonical := func(ref string) string {
		for _, prefix := range []string{"docker-pullable://", "docker://", "containerd://"} {
			ref = strings.TrimPrefix(ref, prefix)
		}
		repo, err := image.Repository(ref)
		if err != nil {
			return ""
		}
		suffix := ""
		if _, digest, ok := strings.Cut(ref, "@"); ok {
			suffix = "@" + digest
		} else {
			last := ref[strings.LastIndex(ref, "/")+1:]
			_, tag, ok := strings.Cut(last, ":")
			if !ok {
				tag = "latest"
			}
			suffix = ":" + tag
		}
		for _, alias := range []string{"docker.io/", "index.docker.io/", "registry-1.docker.io/"} {
			if strings.HasPrefix(repo, alias) {
				repository := strings.TrimPrefix(repo, alias)
				if !strings.Contains(repository, "/") {
					repository = "library/" + repository
				}
				repo = "docker.io/" + repository
				break
			}
		}
		return repo + suffix
	}
	expected := canonical(requested)
	if expected == "" {
		return false
	}
	if _, digest, ok := strings.Cut(requested, "@"); ok {
		rawID := cs.ImageID
		for _, prefix := range []string{"docker-pullable://", "docker://", "containerd://"} {
			rawID = strings.TrimPrefix(rawID, prefix)
		}
		if rawID == digest {
			return true
		}
	}
	return expected == canonical(cs.Image) || expected == canonical(cs.ImageID)
}

func appliedTemplateMatches(obj map[string]any, patch string) bool {
	data, err := yaml.ToJSON([]byte(patch))
	if err != nil {
		return false
	}
	var candidate map[string]any
	if json.Unmarshal(data, &candidate) != nil {
		return false
	}
	proposed, ok := kube.WorkloadPodSpec(candidate)
	if !ok {
		return false
	}
	live, ok := kube.WorkloadPodSpec(obj)
	return ok && patchFieldsMatch(live, proposed)
}
func patchFieldsMatch(live, patch any) bool { return patchFieldsMatchAt(live, patch, "") }
func patchFieldsMatchAt(live, patch any, path string) bool {
	if patch == nil {
		return live == nil
	}
	switch expected := patch.(type) {
	case map[string]any:
		actual, ok := live.(map[string]any)
		if !ok {
			return false
		}
		for key, value := range expected {
			if !patchFieldsMatchAt(actual[key], value, path+"/"+key) {
				return false
			}
		}
		return true
	case []any:
		actual, ok := live.([]any)
		if !ok {
			return false
		}
		for _, item := range expected {
			wanted, isMap := item.(map[string]any)
			if !isMap {
				return reflect.DeepEqual(actual, expected)
			}
			name, hasName := wanted["name"]
			if !hasName {
				return reflect.DeepEqual(actual, expected)
			}
			found := false
			for _, raw := range actual {
				current, ok := raw.(map[string]any)
				if ok && current["name"] == name && patchFieldsMatchAt(current, wanted, path) {
					found = true
					break
				}
			}
			if !found {
				return false
			}
		}
		return true
	default:
		if strings.Contains(path, "/resources/requests/") || strings.Contains(path, "/resources/limits/") {
			actual, errA := resource.ParseQuantity(fmt.Sprint(live))
			expected, errB := resource.ParseQuantity(fmt.Sprint(patch))
			return errA == nil && errB == nil && actual.Cmp(expected) == 0
		}
		return fmt.Sprint(live) == fmt.Sprint(patch)
	}
}

func projectedServiceAccount(volume map[string]any) bool {
	projected, _ := volume["projected"].(map[string]any)
	sources, _ := projected["sources"].([]any)
	for _, raw := range sources {
		source, _ := raw.(map[string]any)
		if source["serviceAccountToken"] != nil {
			return true
		}
	}
	return false
}
func podSpecHasImage(pod kube.Pod, section, name, requested string) bool {
	containers := pod.Spec.Containers
	if section == "initContainers" {
		containers = pod.Spec.InitContainers
	}
	for _, c := range containers {
		if c.Name == name && runtimeImageMatches(requested, kube.ContainerStatus{Image: c.Image, ImageID: "spec-image"}) {
			return true
		}
	}
	return false
}
