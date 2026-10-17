package pdbfix

import (
	"context"
	"encoding/json"
	"errors"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"reflect"
	syaml "sigs.k8s.io/yaml"
	"strings"
)

type Plan struct {
	Resource      string   `json:"resource" yaml:"resource"`
	Namespace     string   `json:"namespace" yaml:"namespace"`
	Status        string   `json:"status" yaml:"status"`
	Diff          string   `json:"diff,omitempty" yaml:"diff,omitempty"`
	Scope         string   `json:"scope" yaml:"scope"`
	ChangedFields []string `json:"changedFields,omitempty" yaml:"changedFields,omitempty"`
	Prerequisites []string `json:"prerequisites,omitempty" yaml:"prerequisites,omitempty"`
	Warnings      []string `json:"warnings,omitempty" yaml:"warnings,omitempty"`
	proof         *proof
}

func Prepare(parent context.Context, r Reader, a *Authority, proposal string) (Plan, error) {
	ctx, cancel := operationContext(parent)
	defer cancel()
	p := Plan{Status: "prerequisite-required", Scope: "unverified", Prerequisites: []string{"Provide independently approved --pdb-policy availability intent."}}
	if a == nil {
		return p, nil
	}
	if e := a.check(); e != nil {
		return p, e
	}
	ns, name := a.intent.Metadata.Namespace, a.intent.Spec.PDB.Name
	p.Resource = "PodDisruptionBudget/" + name
	p.Namespace = ns
	target := a.Target()
	s, e := Collect(ctx, r, ns, name, &target)
	if e != nil {
		return p, e
	}
	candidate := s.PDB.DeepCopy()
	candidate.Spec.Selector = a.intent.Spec.Selector.DeepCopy()
	candidate.Spec.MinAvailable = a.intent.Spec.Budget.MinAvailable
	candidate.Spec.MaxUnavailable = a.intent.Spec.Budget.MaxUnavailable
	candidate = candidate.DeepCopy()
	if reflect.DeepEqual(candidate.Spec, s.PDB.Spec) {
		return p, errors.New("approved PDB intent already matches; no correction needed")
	}
	if e = eligible(s, a, candidate); e != nil {
		return p, e
	}
	for _, f := range []string{"selector", "minAvailable", "maxUnavailable"} {
		if hash(specValue(s.PDB, f)) != hash(specValue(candidate, f)) {
			p.ChangedFields = append(p.ChangedFields, f)
		}
	}
	p.Status = "candidate-prepared"
	p.Prerequisites = nil
	if sourceOwned(s.PDB) {
		p.Status = "source-review-required"
		p.Warnings = append(p.Warnings, "Source-managed PDB: direct cluster delivery blocked; use reviewed source.")
	}
	p.proof = &proof{authority: a, original: s.PDB.DeepCopy(), candidate: candidate.DeepCopy(), snapshot: s, target: target, sourceManaged: sourceOwned(s.PDB)}
	raw, e := p.PatchYAML()
	if e != nil {
		return p, e
	}
	p.Diff = raw
	p.proof.planBinding = hash(p)
	if proposal != "" {
		if e = validateProposal(proposal, p); e != nil {
			return Plan{}, e
		}
	}
	return p, nil
}
func specValue(p *policyv1.PodDisruptionBudget, f string) any {
	b, _ := json.Marshal(p.Spec)
	var s map[string]any
	json.Unmarshal(b, &s)
	return s[f]
}
func (p Plan) PatchYAML() (string, error) {
	if p.proof == nil {
		return "", errors.New("no concrete PDB correction available")
	}
	fields := map[string]any{}
	for _, f := range p.ChangedFields {
		fields[f] = specValue(p.proof.candidate, f)
	}
	b, e := json.Marshal(map[string]any{"apiVersion": "policy/v1", "kind": "PodDisruptionBudget", "metadata": map[string]string{"name": p.proof.candidate.Name, "namespace": p.proof.candidate.Namespace}, "spec": fields})
	if e != nil {
		return "", e
	}
	b, e = syaml.JSONToYAML(b)
	return string(b), e
}
func validateProposal(raw string, p Plan) error {
	var n map[string]any
	b, e := syaml.YAMLToJSONStrict([]byte(raw))
	if e != nil {
		return e
	}
	if e = json.Unmarshal(b, &n); e != nil {
		return e
	}
	for k := range n {
		if k != "apiVersion" && k != "kind" && k != "metadata" && k != "spec" {
			return errors.New("AI patch changes unsupported PDB field")
		}
	}
	if n["apiVersion"] != "policy/v1" || n["kind"] != "PodDisruptionBudget" {
		return errors.New("AI PDB identity invalid")
	}
	m, ok := n["metadata"].(map[string]any)
	if !ok || m["name"] != p.proof.candidate.Name || m["namespace"] != p.Namespace || len(m) != 2 {
		return errors.New("AI PDB metadata changes are forbidden")
	}
	spec, ok := n["spec"].(map[string]any)
	if !ok || len(spec) == 0 {
		return errors.New("AI patch has no correction")
	}
	for k, v := range spec {
		allowed := false
		for _, f := range p.ChangedFields {
			if f == k {
				allowed = true
			}
		}
		if !allowed || hash(v) != hash(specValue(p.proof.candidate, k)) {
			return errors.New("AI patch differs from approved PDB correction")
		}
	}
	return nil
}
func sourceOwned(p *policyv1.PodDisruptionBudget) bool {
	if len(p.OwnerReferences) > 0 {
		return true
	}
	for k, v := range p.Labels {
		key := strings.ToLower(k)
		if (key == "app.kubernetes.io/managed-by" && v != "") || strings.Contains(key, "argocd") || strings.Contains(key, "fluxcd") {
			return true
		}
	}
	for k := range p.Annotations {
		key := strings.ToLower(k)
		if strings.Contains(key, "helm") || strings.Contains(key, "argocd") || strings.Contains(key, "fluxcd") {
			return true
		}
	}
	return false
}
func stableWorkload(s Snapshot, a *Authority) (int32, string, error) {
	w := s.Workload
	if w == nil {
		return 0, "", errors.New("workload evidence missing")
	}
	b, e := json.Marshal(w)
	if e != nil {
		return 0, "", e
	}
	var meta metav1.ObjectMeta
	switch a.Target().Kind {
	case "Deployment":
		var d appsv1.Deployment
		if e = json.Unmarshal(b, &d); e != nil {
			return 0, "", e
		}
		meta = d.ObjectMeta
		n := int32(1)
		if d.Spec.Replicas != nil {
			n = *d.Spec.Replicas
		}
		if d.Spec.Paused || d.Status.ObservedGeneration != d.Generation || d.Status.Replicas != n || d.Status.ReadyReplicas != n || d.Status.AvailableReplicas != n || d.Status.UpdatedReplicas != n {
			return 0, "", errors.New("Deployment rollout/scale is not complete")
		}
		if e = checkWorkloadIdentity(w, meta, a); e != nil {
			return 0, "", e
		}
		return n, string(meta.UID), nil
	case "StatefulSet":
		var d appsv1.StatefulSet
		if e = json.Unmarshal(b, &d); e != nil {
			return 0, "", e
		}
		meta = d.ObjectMeta
		n := int32(1)
		if d.Spec.Replicas != nil {
			n = *d.Spec.Replicas
		}
		if d.Spec.UpdateStrategy.Type != appsv1.RollingUpdateStatefulSetStrategyType || d.Spec.UpdateStrategy.RollingUpdate != nil && d.Spec.UpdateStrategy.RollingUpdate.Partition != nil && *d.Spec.UpdateStrategy.RollingUpdate.Partition != 0 || d.Status.ObservedGeneration != d.Generation || d.Status.Replicas != n || d.Status.ReadyReplicas != n || d.Status.CurrentReplicas != n || d.Status.UpdatedReplicas != n || d.Status.CurrentRevision == "" || d.Status.CurrentRevision != d.Status.UpdateRevision {
			return 0, "", errors.New("StatefulSet must have a completed default RollingUpdate")
		}
		if e = checkWorkloadIdentity(w, meta, a); e != nil {
			return 0, "", e
		}
		return n, string(meta.UID), nil
	}
	return 0, "", errors.New("unsupported workload")
}
func checkWorkloadIdentity(w map[string]any, m metav1.ObjectMeta, a *Authority) error {
	if w["apiVersion"] != "apps/v1" || w["kind"] != a.Target().Kind || m.Name != a.Target().Name || m.Namespace != a.intent.Metadata.Namespace || m.UID == "" || m.DeletionTimestamp != nil {
		return errors.New("workload identity differs from approved target")
	}
	return nil
}
func eligible(s Snapshot, a *Authority, c *policyv1.PodDisruptionBudget) error {
	return eligibility(s, a, c, true)
}
func eligibility(s Snapshot, a *Authority, c *policyv1.PodDisruptionBudget, requireStatus bool) error {
	if len(s.Issues) > 0 {
		return errors.New("complete PDB, Pod, owner and HPA evidence required")
	}
	if s.PDB.DeletionTimestamp != nil || s.PDB.Generation < 1 || requireStatus && s.PDB.Status.ObservedGeneration != s.PDB.Generation {
		return errors.New("current-generation PDB reconciliation required")
	}
	for _, cond := range s.PDB.Status.Conditions {
		if cond.Reason == "SyncFailed" {
			return errors.New("PDB reconciliation failed")
		}
	}
	if len(s.PDB.Status.DisruptedPods) > 0 {
		return errors.New("in-flight disruptions block repair")
	}
	n, uid, e := stableWorkload(s, a)
	if e != nil {
		return e
	}
	if n < 1 {
		return errors.New("positive desired replicas required")
	}
	for _, h := range s.HPAs {
		if strings.EqualFold(h.Spec.ScaleTargetRef.Kind, a.Target().Kind) && h.Spec.ScaleTargetRef.Name == a.Target().Name {
			return errors.New("active HPA scale target requires separate scaling verification")
		}
	}
	current, chosen, owned := 0, 0, 0
	for _, pod := range s.Pods {
		old, new := selected(s.PDB.Spec.Selector, pod.Labels), selected(c.Spec.Selector, pod.Labels)
		if old {
			current++
		}
		o, ok := s.Owners[string(pod.UID)]
		isOwner := ok && o.Kind == a.Target().Kind && o.Name == a.Target().Name && o.UID == uid
		if isOwner {
			owned++
		}
		if isOwner && !new {
			return errors.New("approved selector omits intended workload Pods")
		}
		if new {
			chosen++
			if !isOwner || !ready(pod) || !workloadSelects(s.Workload, pod.Labels) {
				return errors.New("candidate selects unready, unrelated or unverified Pods")
			}
			for _, p := range s.Budgets {
				if p.UID != s.PDB.UID && selected(p.Spec.Selector, pod.Labels) {
					return errors.New("overlapping PDB blocks repair")
				}
			}
		}
		if old && !isOwner {
			return errors.New("current selector protects unrelated Pods")
		}
	}
	if chosen != int(n) || owned != int(n) {
		return errors.New("candidate must select the complete stable workload Pod set")
	}
	if !reflect.DeepEqual(s.PDB.Spec.Selector, c.Spec.Selector) && current != 0 {
		return errors.New("selector correction only allowed for currently empty selections")
	}
	found := false
	for _, p := range s.Budgets {
		if p.UID == s.PDB.UID {
			if p.Name != s.PDB.Name || hash(p.Spec) != hash(s.PDB.Spec) {
				return errors.New("PDB inventory changed during collection")
			}
			found = true
		}
	}
	if !found {
		return errors.New("PDB inventory does not contain target identity")
	}
	effect, e := EvaluateBudget(c.Spec, n, n)
	if e != nil {
		return e
	}
	if effect.RequiredHealthy > n {
		return errors.New("approved budget requires more healthy replicas than exist")
	}
	if effect.RequiredHealthy < *a.intent.Spec.Availability.MinimumHealthyReplicas || effect.AllowedDisruptions > *a.intent.Spec.Availability.MaximumConcurrentDisruptions {
		return errors.New("candidate violates approved availability floor or disruption ceiling")
	}
	return nil
}
func selectedPods(s Snapshot, a *Authority) []corev1.Pod {
	var ps []corev1.Pod
	for _, p := range s.Pods {
		if selected(a.intent.Spec.Selector, p.Labels) || selected(s.PDB.Spec.Selector, p.Labels) || workloadSelects(s.Workload, p.Labels) {
			ps = append(ps, p)
		}
	}
	return ps
}
