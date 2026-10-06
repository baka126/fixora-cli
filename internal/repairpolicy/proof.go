// Package repairpolicy keeps repair authority and verification receipts in memory.
// JSON output describes a decision; it cannot recreate permission to execute it.
package repairpolicy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/fixora/kubectl-fixora/internal/image"
	"github.com/fixora/kubectl-fixora/internal/kube"
	"gopkg.in/yaml.v3"
)

type Target struct{ Kind, Name, Namespace, UID, PodName, PodUID string }
type Snapshot struct {
	Target    Target
	Object    map[string]any
	Pod       *kube.Pod
	Namespace map[string]any
	Nodes     []kube.Node
	Events    []kube.Event
}
type Summary struct {
	Family        string   `json:"family"`
	Result        string   `json:"result"`
	Detail        string   `json:"detail"`
	ChangedFields []string `json:"changedFields,omitempty"`
	Caveats       []string `json:"caveats,omitempty"`
}
type Authority struct {
	object map[string]any
	digest string
}
type Proof struct {
	inspect                               func(context.Context, string) (image.Result, error)
	placement                             map[string]any
	nodes                                 map[string]string
	platforms                             map[string]image.Result
	baseline                              *Authority
	family, binding, candidate, authority string
	target                                Target
	summary                               Summary
}
type Receipt struct {
	binding, candidate, witness string
	summary                     Summary
}
type ValidatePatchFunc func(patch, strategy, resource, namespace string) error

func Collect(ctx context.Context, r kube.Reader, target Target) (Snapshot, error) {
	if r == nil {
		return Snapshot{}, errors.New("repair policy requires a cluster reader")
	}
	obj, err := r.GetResource(ctx, target.Namespace, strings.ToLower(target.Kind)+"/"+target.Name)
	if err != nil {
		return Snapshot{}, err
	}
	meta, _ := obj["metadata"].(map[string]any)
	uid, _ := meta["uid"].(string)
	if uid == "" || obj["kind"] != target.Kind || obj["apiVersion"] != kube.WorkloadVersion(target.Kind) || meta["name"] != target.Name || meta["namespace"] != target.Namespace || (target.UID != "" && target.UID != uid) {
		return Snapshot{}, errors.New("repair target identity is missing or changed")
	}
	if _, ok := kube.WorkloadPodSpec(obj); !ok {
		return Snapshot{}, errors.New("repair target has no unambiguous Pod template")
	}
	target.UID = uid
	s := Snapshot{Target: target, Object: copyObject(obj)}
	if target.PodName != "" {
		pod, err := r.GetPod(ctx, target.Namespace, target.PodName)
		if err != nil {
			return Snapshot{}, err
		}
		if pod.Metadata.UID == "" || pod.Metadata.Name != target.PodName || pod.Metadata.Namespace != target.Namespace || (target.PodUID != "" && target.PodUID != pod.Metadata.UID) {
			return Snapshot{}, errors.New("repair Pod identity is missing or changed")
		}
		if target.Kind == "Pod" {
			if pod.Metadata.UID != uid {
				return Snapshot{}, errors.New("target Pod was replaced")
			}
		} else {
			owner, err := kube.ResolveWorkloadOwner(ctx, r, pod)
			if err != nil {
				return Snapshot{}, err
			}
			if !owner.Matches(target.Kind, target.Name, uid) {
				return Snapshot{}, errors.New("Pod controlling owner does not match repair target")
			}
		}
		s.Target.PodUID = pod.Metadata.UID
		raw, _ := json.Marshal(pod)
		var captured kube.Pod
		_ = json.Unmarshal(raw, &captured)
		s.Pod = &captured
	}
	return s, nil
}
func BindCommon(s Snapshot, patch, strategy string, validate ValidatePatchFunc) (*Proof, error) {
	switch strategy {
	case "image", "fix-architecture", "resources", "env", "probe", "filesystem":
	default:
		return nil, errors.New("strategy requires a dedicated repair policy")
	}
	if validate == nil {
		return nil, errors.New("candidate validator is required")
	}
	if err := validate(patch, strategy, s.Target.Kind+"/"+s.Target.Name, s.Target.Namespace); err != nil {
		return nil, err
	}
	return bind(s, patch, strategy, nil, nil)
}
func bind(s Snapshot, patch, family string, a *Authority, fields []string) (*Proof, error) {
	binding, err := snapshotBinding(s)
	if err != nil {
		return nil, err
	}
	authority := ""
	if a != nil {
		authority = a.digest
		if authority == "" || authority != digest(a.object) {
			return nil, errors.New("invalid repair authority")
		}
	}
	return &Proof{family: family, binding: binding, candidate: digest(patch), authority: authority, baseline: a, target: s.Target, summary: Summary{Family: family, Result: "bound", Detail: "Candidate bound to the inspected workload; verification is required.", ChangedFields: append([]string(nil), fields...)}}, nil
}
func snapshotBinding(s Snapshot) (string, error) {
	meta, _ := s.Object["metadata"].(map[string]any)
	spec, ok := kube.WorkloadPodSpec(s.Object)
	if !ok || s.Target.UID == "" || meta["uid"] != s.Target.UID || meta["name"] != s.Target.Name || meta["namespace"] != s.Target.Namespace || s.Object["kind"] != s.Target.Kind || s.Object["apiVersion"] != kube.WorkloadVersion(s.Target.Kind) {
		return "", errors.New("invalid repair snapshot identity")
	}
	var podSpec any
	if s.Pod != nil {
		if s.Pod.Metadata.UID == "" || s.Pod.Metadata.UID != s.Target.PodUID || s.Pod.Metadata.Name != s.Target.PodName || s.Pod.Metadata.Namespace != s.Target.Namespace {
			return "", errors.New("invalid repair Pod binding")
		}
		podSpec = s.Pod.Spec
	}
	return digest([]any{s.Target, spec, podSpec}), nil
}
func (p *Proof) Check(s Snapshot, patch string) error {
	if p == nil || p.binding == "" || p.candidate == "" {
		return errors.New("fresh in-memory repair proof is required")
	}
	binding, err := snapshotBinding(s)
	if err != nil {
		return err
	}
	if p.binding != binding || p.candidate != digest(patch) {
		return errors.New("workload or candidate changed since repair proof was collected")
	}
	return nil
}
func (p *Proof) Summary() Summary {
	if p == nil {
		return Summary{}
	}
	s := p.summary
	s.ChangedFields = append([]string(nil), s.ChangedFields...)
	s.Caveats = append([]string(nil), s.Caveats...)
	return s
}
func (p *Proof) Target() Target {
	if p == nil {
		return Target{}
	}
	return p.target
}
func (p *Proof) Family() string {
	if p == nil {
		return ""
	}
	return p.family
}
func (r *Receipt) Check(p *Proof, patch string) error {
	if r == nil || p == nil || p.binding == "" || r.binding == "" || r.witness == "" || r.binding != digest([]string{p.binding, p.authority, p.family}) || r.candidate != p.candidate || r.candidate != digest(patch) {
		return errors.New("matching in-memory verification receipt is required")
	}
	return nil
}
func (r *Receipt) Summary() Summary {
	if r == nil {
		return Summary{}
	}
	return r.summary
}
func VerifyCommon(ctx context.Context, r kube.Reader, p *Proof, pod *kube.Pod, patch string) (*Receipt, error) {
	if p == nil {
		return nil, errors.New("repair proof is missing")
	}
	switch p.family {
	case "image", "fix-architecture", "resources", "env", "probe", "filesystem":
	default:
		return nil, errors.New("dedicated family verifier required")
	}
	return witness(ctx, r, p, pod, patch)
}

// Called only after the shadow engine's readiness/soak verification succeeds.
func witness(ctx context.Context, r kube.Reader, p *Proof, pod *kube.Pod, patch string) (*Receipt, error) {
	if p == nil || p.binding == "" || p.candidate != digest(patch) || r == nil || pod == nil {
		return nil, errors.New("invalid verification witness")
	}
	live, err := r.GetPod(ctx, pod.Metadata.Namespace, pod.Metadata.Name)
	if err != nil {
		return nil, err
	}
	if live.Metadata.UID == "" || live.Metadata.UID != pod.Metadata.UID || live.Metadata.Namespace != p.target.Namespace || live.Metadata.Labels["fixora.io/sandbox"] != "true" || live.Metadata.Labels["fixora.io/session"] == "" || live.Metadata.Labels["fixora.io/session"] != pod.Metadata.Labels["fixora.io/session"] || live.Metadata.UID == p.target.PodUID || !reflect.DeepEqual(live.Spec, pod.Spec) {
		return nil, errors.New("shadow witness identity or specification changed")
	}
	ready := false
	for _, c := range live.Status.Conditions {
		if c.Type == "Ready" && c.Status == "True" {
			ready = true
		}
	}
	complete := live.Status.Phase == "Succeeded" && (p.target.Kind == "Job" || p.target.Kind == "CronJob")
	if (!complete && (!ready || live.Status.Phase != "Running")) || len(live.Status.ContainerStatuses) != len(live.Spec.Containers) {
		return nil, errors.New("shadow witness has no healthy runtime or successful batch completion")
	}
	for _, c := range live.Status.ContainerStatuses {
		if complete {
			term, ok := c.State["terminated"]
			if !ok || term.ExitCode != 0 {
				return nil, errors.New("shadow batch did not complete successfully")
			}
		} else if !c.Ready {
			return nil, errors.New("shadow container is not Ready")
		}
	}
	var candidate map[string]any
	if err := yaml.Unmarshal([]byte(patch), &candidate); err != nil {
		return nil, errors.New("invalid witness candidate")
	}
	changes, ok := kube.WorkloadPodSpec(candidate)
	if !ok {
		return nil, errors.New("invalid witness candidate envelope")
	}
	observed := copyValue(live.Spec)
	if !matchesFields(changes, observed) {
		return nil, errors.New("shadow Pod does not contain the approved candidate fields")
	}
	return &Receipt{binding: digest([]string{p.binding, p.authority, p.family}), candidate: p.candidate, witness: digest([]any{live.Metadata.UID, live.Metadata.Namespace, live.Metadata.Labels["fixora.io/session"], live.Spec}), summary: Summary{Family: p.family, Result: "verified", Detail: "Exact candidate verified against a live shadow Pod."}}, nil
}
func digest(v any) string {
	raw, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	d := sha256.Sum256(raw)
	return hex.EncodeToString(d[:])
}
func copyObject(v map[string]any) map[string]any {
	raw, _ := json.Marshal(v)
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return out
}
func identityMatches(obj map[string]any, t Target) error {
	meta, _ := obj["metadata"].(map[string]any)
	if obj["kind"] != t.Kind || obj["apiVersion"] != kube.WorkloadVersion(t.Kind) || meta["name"] != t.Name || meta["namespace"] != t.Namespace {
		return fmt.Errorf("baseline workload identity must match %s/%s (%s)", t.Kind, t.Name, t.Namespace)
	}
	return nil
}

// matchesFields checks named strategic-merge lists without treating a subset of
// an ordered command/args array as sufficient evidence.
func matchesFields(expected, actual any) bool {
	switch want := copyValue(expected).(type) {
	case map[string]any:
		got, ok := copyValue(actual).(map[string]any)
		if !ok {
			return false
		}
		for k, v := range want {
			value, exists := got[k]
			if v == nil {
				if exists && value != nil && value != "" {
					return false
				}
				continue
			}
			if !exists || !matchesFields(v, value) {
				return false
			}
		}
		return true
	case []any:
		got, ok := copyValue(actual).([]any)
		if !ok {
			return len(want) == 0 && actual == nil
		}
		named := len(want) > 0
		for _, v := range want {
			m, ok := v.(map[string]any)
			if !ok || m["name"] == nil {
				named = false
				break
			}
		}
		if !named {
			return reflect.DeepEqual(want, got)
		}
		for _, v := range want {
			m := v.(map[string]any)
			found := false
			for _, item := range got {
				gm, ok := item.(map[string]any)
				if ok && gm["name"] == m["name"] {
					if found || !matchesFields(m, gm) {
						return false
					}
					found = true
				}
			}
			if !found {
				return false
			}
		}
		return true
	default:
		return reflect.DeepEqual(copyValue(expected), copyValue(actual))
	}
}

// Revalidate reads current identity and template; no restored JSON state is used.
func Revalidate(ctx context.Context, r kube.Reader, p *Proof, patch string) error {
	if p == nil || p.binding == "" {
		return errors.New("fresh in-memory repair proof is required")
	}
	s, err := Collect(ctx, r, p.target)
	if err != nil {
		return err
	}
	if err = p.Check(s, patch); err != nil {
		return err
	}
	if p.family == "restore-command" {
		_, _, err = Startup(s, p.baseline, "")
		return err
	}
	if p.family == "restore-placement" {
		s.Nodes, err = r.GetNodes(ctx)
		if err != nil {
			return err
		}
		for name, binding := range p.nodes {
			found := false
			for _, node := range s.Nodes {
				if node.Metadata.Name == name && nodeBinding(node) == binding {
					found = true
				}
			}
			if !found {
				return errors.New("approved node identity, labels or taints changed")
			}
		}
		fresh, validationErr := Placement(ctx, s, p.baseline, patch, p.inspect)
		err = validationErr
		if err == nil && digest(fresh.platforms) != digest(p.platforms) {
			return errors.New("image platform evidence changed")
		}

		if err != nil {
			return err
		}
		return CheckPlacementCapacity(ctx, r, p)
	}

	return nil
}

func Dedicated(strategy string) bool {
	return strategy == "restore-command" || strategy == "restore-placement" || strategy == "security-strengthen"
}

// CheckLivePod checks a production readback without minting a shadow receipt.
// The caller must first prove controlling ownership and rollout completion.
func CheckLivePod(ctx context.Context, r kube.Reader, p *Proof, pod *kube.Pod, patch string) error {
	if p == nil || p.binding == "" || p.candidate != digest(patch) || pod == nil || pod.Metadata.UID == "" || pod.Metadata.Namespace != p.target.Namespace {
		return errors.New("invalid live policy witness")
	}
	ready := false
	for _, c := range pod.Status.Conditions {
		if c.Type == "Ready" && c.Status == "True" {
			ready = true
		}
	}
	if !ready || pod.Status.Phase != "Running" {
		return errors.New("updated Pod is not Ready")
	}
	for _, c := range pod.Status.ContainerStatuses {
		if !c.Ready {
			return errors.New("updated container is not Ready")
		}
	}
	obj, err := r.GetResource(ctx, pod.Metadata.Namespace, "pod/"+pod.Metadata.Name)
	if err != nil {
		return err
	}
	meta := mapAny(obj["metadata"])
	if meta["uid"] != pod.Metadata.UID {
		return errors.New("live Pod was replaced")
	}
	observed, ok := kube.WorkloadPodSpec(obj)
	if !ok {
		return errors.New("live Pod spec is unavailable")
	}
	_, changes, err := policyPatch(patch, p.target)
	if err != nil {
		return err
	}
	if !matchesFields(changes, observed) {
		return errors.New("admitted live Pod differs from the approved fields")
	}
	if p.family == "restore-placement" {
		nodes, err := r.GetNodes(ctx)
		if err != nil {
			return err
		}
		for _, node := range nodes {
			if node.Metadata.Name == pod.Spec.NodeName && nodeEligible(node, p.placement, p.platforms) {
				return nil
			}
		}
		return errors.New("updated Pod is outside the approved eligible placement")
	}
	return nil
}
