package repairpolicy

import (
	"context"
	"strings"
	"testing"

	"github.com/fixora/kubectl-fixora/internal/image"
	"github.com/fixora/kubectl-fixora/internal/kube"
	"gopkg.in/yaml.v3"
)

func placementFixture(t *testing.T) (Snapshot, *Authority, string) {
	s := startupSnapshot(t, "Deployment")
	spec, _ := kube.WorkloadPodSpec(s.Object)
	spec["nodeSelector"] = map[string]any{"pool": "missing"}
	s.Pod.Spec.NodeSelector = map[string]string{"pool": "missing"}
	s.Pod.Status = kube.PodStatus{Phase: "Pending", Conditions: []kube.Condition{{Type: "PodScheduled", Status: "False", Reason: "Unschedulable", Message: "nodes didn't match Pod's node affinity/selector"}}}
	node := kube.Node{Metadata: kube.ObjectMeta{Name: "node-a", UID: "node-1", Labels: map[string]string{"pool": "approved", "kubernetes.io/os": "linux", "kubernetes.io/arch": "amd64"}}}
	node.Status.Conditions = []kube.Condition{{Type: "Ready", Status: "True"}}
	node.Status.Allocatable = map[string]string{"cpu": "4", "memory": "4Gi", "pods": "100"}
	s.Nodes = []kube.Node{node}
	obj := copyObject(s.Object)
	bs, _ := kube.WorkloadPodSpec(obj)
	bs["nodeSelector"] = map[string]any{"pool": "approved"}
	raw, _ := yaml.Marshal(obj)
	a, err := ReadBaseline(baselineFile(t, string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	patchObj, _ := kube.WorkloadPatch("Deployment", "worker", "demo", map[string]any{"nodeSelector": map[string]any{"pool": "approved"}})
	raw, _ = yaml.Marshal(patchObj)
	return s, a, string(raw)
}
func inspectLinux(context.Context, string) (image.Result, error) {
	return image.Result{Platforms: []image.Platform{{OS: "linux", Architecture: "amd64"}}}, nil
}
func TestPlacementApprovedSelectorAffinityAndExactToleration(t *testing.T) {
	s, a, patch := placementFixture(t)
	p, err := Placement(context.Background(), s, a, patch, inspectLinux)
	if err != nil {
		t.Fatal(err)
	}
	if err = p.Check(s, patch); err != nil {
		t.Fatal(err)
	}
}
func TestPlacementCannotBroadenUnapprovedIntent(t *testing.T) {
	s, a, patch := placementFixture(t)
	if _, err := Placement(context.Background(), s, a, strings.Replace(patch, "approved", "other", 1), inspectLinux); err == nil {
		t.Fatal("unapproved destination accepted")
	}
}
func TestPlacementUnsupportedRemainingConstraintsRefuse(t *testing.T) {
	for _, field := range []string{"nodeName", "schedulerName", "topologySpreadConstraints", "priorityClassName", "volumes"} {
		t.Run(field, func(t *testing.T) {
			s, a, patch := placementFixture(t)
			spec, _ := kube.WorkloadPodSpec(s.Object)
			spec[field] = "unsupported"
			if _, err := Placement(context.Background(), s, a, patch, inspectLinux); err == nil {
				t.Fatal("unsupported constraint accepted")
			}
		})
	}
}
func TestPlacementRequiresCurrentMatchingSchedulerEvidence(t *testing.T) {
	s, a, patch := placementFixture(t)
	s.Pod.Status.Conditions = nil
	if _, err := Placement(context.Background(), s, a, patch, inspectLinux); err == nil {
		t.Fatal("missing scheduler evidence accepted")
	}
	s, a, patch = placementFixture(t)
	s.Pod.Spec.NodeSelector["pool"] = "stale"
	if _, err := Placement(context.Background(), s, a, patch, inspectLinux); err == nil {
		t.Fatal("stale revision accepted")
	}
}
func TestPlacementShadowBindsEligibleSchedulerSelectedNode(t *testing.T) {
	s, a, patch := placementFixture(t)
	p, err := Placement(context.Background(), s, a, patch, inspectLinux)
	if err != nil {
		t.Fatal(err)
	}
	pod := *s.Pod
	pod.Metadata = kube.ObjectMeta{Name: "shadow", Namespace: "demo", UID: "shadow-1", Labels: map[string]string{"fixora.io/sandbox": "true", "fixora.io/session": "session"}}
	pod.Spec.NodeSelector = map[string]string{"pool": "approved"}
	pod.Spec.NodeName = "node-a"
	pod.Status = kube.PodStatus{Phase: "Running", Conditions: []kube.Condition{{Type: "Ready", Status: "True"}}, ContainerStatuses: []kube.ContainerStatus{{Name: "app", Ready: true}}}
	r := placementReader{witnessReader: witnessReader{pod: pod}, nodes: s.Nodes}
	if _, err = VerifyPlacement(context.Background(), r, p, &pod, patch); err != nil {
		t.Fatal(err)
	}
	r.nodes[0].Metadata.Labels["pool"] = "other"
	if _, err = VerifyPlacement(context.Background(), r, p, &pod, patch); err == nil {
		t.Fatal("node labels changed but receipt accepted")
	}
}

type placementReader struct {
	witnessReader
	nodes []kube.Node
}

func (r placementReader) GetNodes(context.Context) ([]kube.Node, error) { return r.nodes, nil }

func TestPlacementApprovedAffinityAndTolerations(t *testing.T) {
	for _, family := range []string{"affinity", "tolerations"} {
		t.Run(family, func(t *testing.T) {
			s, _, _ := placementFixture(t)
			spec, _ := kube.WorkloadPodSpec(s.Object)
			delete(spec, "nodeSelector")
			s.Pod.Spec.NodeSelector = nil
			changes := map[string]any{}
			if family == "affinity" {
				old := map[string]any{"nodeAffinity": map[string]any{"requiredDuringSchedulingIgnoredDuringExecution": map[string]any{"nodeSelectorTerms": []any{map[string]any{"matchExpressions": []any{map[string]any{"key": "pool", "operator": "In", "values": []any{"missing"}}}}}}}}
				spec["affinity"] = old
				s.Pod.Spec.Affinity = copyObject(old)
				next := copyObject(old)
				next["nodeAffinity"].(map[string]any)["requiredDuringSchedulingIgnoredDuringExecution"].(map[string]any)["nodeSelectorTerms"].([]any)[0].(map[string]any)["matchExpressions"].([]any)[0].(map[string]any)["values"] = []any{"approved"}
				changes[family] = next
			} else {
				s.Pod.Status.Conditions[0].Message = "nodes had untolerated taint {tenant: blue}"
				s.Nodes[0].Spec.Taints = []map[string]any{{"key": "tenant", "value": "blue", "effect": "NoSchedule"}}
				changes[family] = []any{map[string]any{"key": "tenant", "value": "blue", "effect": "NoSchedule", "operator": "Equal"}}
			}
			approved := copyObject(s.Object)
			base, _ := kube.WorkloadPodSpec(approved)
			for k, v := range changes {
				base[k] = v
			}
			raw, _ := yaml.Marshal(approved)
			a, err := ReadBaseline(baselineFile(t, string(raw)))
			if err != nil {
				t.Fatal(err)
			}
			obj, _ := kube.WorkloadPatch("Deployment", "worker", "demo", changes)
			raw, _ = yaml.Marshal(obj)
			if _, err = Placement(context.Background(), s, a, string(raw), inspectLinux); err != nil {
				t.Fatal(err)
			}
		})
	}
}
func TestPlacementBaselineDeletesObsoleteSelectorWithoutRemovingOtherFields(t *testing.T) {
	s, a, _ := placementFixture(t)
	spec, _ := kube.WorkloadPodSpec(s.Object)
	spec["nodeSelector"].(map[string]any)["old"] = "unreachable"
	s.Pod.Spec.NodeSelector["old"] = "unreachable"
	patch, err := Candidate(s, a, "placement")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(patch, "old: null") {
		t.Fatal(patch)
	}
	if _, err = Placement(context.Background(), s, a, patch, inspectLinux); err != nil {
		t.Fatal(err)
	}
}
func TestPlacementRefusesWrongPlatformAndUnresolvedCapacity(t *testing.T) {
	s, a, patch := placementFixture(t)
	if _, err := Placement(context.Background(), s, a, patch, func(context.Context, string) (image.Result, error) {
		return image.Result{Platforms: []image.Platform{{OS: "linux", Architecture: "arm64"}}}, nil
	}); err == nil {
		t.Fatal("incompatible platform accepted")
	}
	s.Pod.Status.Conditions[0].Message += "; Insufficient memory"
	if _, err := Placement(context.Background(), s, a, patch, inspectLinux); err == nil {
		t.Fatal("remaining capacity block accepted")
	}
}

func TestPlacementReceiptPreservesExistingAdmissionTolerations(t *testing.T) {
	s, a, patch := placementFixture(t)
	spec, _ := kube.WorkloadPodSpec(s.Object)
	spec["nodeSelector"] = map[string]any{"pool": "approved"}
	s.Pod.Spec.NodeSelector = map[string]string{"pool": "approved"}
	s.Pod.Status.Conditions[0].Message = "nodes had untolerated taint {tenant: blue}"
	s.Nodes[0].Spec.Taints = []map[string]any{{"key": "tenant", "value": "blue", "effect": "NoSchedule"}}
	implicit := map[string]any{"key": "node.kubernetes.io/not-ready", "operator": "Exists", "effect": "NoExecute", "tolerationSeconds": float64(300)}
	s.Pod.Spec.Tolerations = []map[string]any{implicit}
	tolerance := map[string]any{"key": "tenant", "value": "blue", "effect": "NoSchedule", "operator": "Equal"}
	obj := copyObject(s.Object)
	base, _ := kube.WorkloadPodSpec(obj)
	base["tolerations"] = []any{tolerance}
	raw, _ := yaml.Marshal(obj)
	var err error
	a, err = ReadBaseline(baselineFile(t, string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	patchObj, _ := kube.WorkloadPatch("Deployment", "worker", "demo", map[string]any{"tolerations": []any{tolerance}})
	raw, _ = yaml.Marshal(patchObj)
	patch = string(raw)
	p, err := Placement(context.Background(), s, a, patch, inspectLinux)
	if err != nil {
		t.Fatal(err)
	}
	pod := *s.Pod
	pod.Metadata = kube.ObjectMeta{Name: "shadow", UID: "shadow-1", Namespace: "demo", Labels: map[string]string{"fixora.io/session": "session", "fixora.io/sandbox": "true"}}
	pod.Spec.Tolerations = []map[string]any{tolerance, implicit}
	pod.Spec.NodeName = "node-a"
	pod.Status = kube.PodStatus{Phase: "Running", Conditions: []kube.Condition{{Type: "Ready", Status: "True"}}, ContainerStatuses: []kube.ContainerStatus{{Name: "app", Ready: true}}}
	r := placementReader{witnessReader: witnessReader{pod: pod}, nodes: s.Nodes}
	if _, err = VerifyPlacement(context.Background(), r, p, &pod, patch); err != nil {
		t.Fatal(err)
	}
}
