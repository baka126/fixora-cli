package servicefix

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"

	"github.com/fixora/kubectl-fixora/internal/analyzer"
	"gopkg.in/yaml.v3"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/strategicpatch"
	syaml "sigs.k8s.io/yaml"
)

type Plan struct {
	Status        string   `json:"status"`
	Resource      string   `json:"resource"`
	Namespace     string   `json:"namespace"`
	Diff          string   `json:"diff,omitempty"`
	Scope         string   `json:"scope"`
	ChangedFields []string `json:"changedFields,omitempty"`
	Prerequisites []string `json:"prerequisites,omitempty"`
	Warnings      []string `json:"warnings,omitempty"`
	Proof         *Proof   `json:"-" yaml:"-"`
}

func Prepare(ctx context.Context, r Reader, a *Authority, f analyzer.Finding, proposal string) (Plan, error) {
	ctx, cancel := operationContext(ctx)
	defer cancel()
	out := Plan{Status: "prerequisite-required", Resource: f.ResourceKind + "/" + f.ResourceName, Namespace: f.Namespace, Scope: "unverified"}
	if a == nil {
		return out, errors.New("approved Service policy required")
	}
	p := a.intent
	if f.ResourceKind != "Service" || f.ResourceName != p.Spec.Service.Name || f.Namespace != p.Metadata.Namespace || f.Recovered || f.Status == "Healthy" {
		return out, errors.New("diagnosis is not an exact failing approved Service")
	}
	s, err := Collect(ctx, r, a)
	if err != nil {
		return out, err
	}
	if f.ResourceUID != "" && f.ResourceUID != string(s.Service.UID) {
		return out, errors.New("Service was replaced after diagnosis")
	}
	c := s.Service.DeepCopy()
	c.Spec.Selector = map[string]string{}
	for k, v := range p.Spec.Selector {
		c.Spec.Selector[k] = v
	}
	index := -1
	for i, port := range c.Spec.Ports {
		if port.Name == p.Spec.Port.Name && port.Port == p.Spec.Port.Port {
			index = i
		}
	}
	c.Spec.Ports[index].TargetPort = p.Spec.Port.TargetPort
	if proposal != "" {
		if f.AI == nil || f.AI.Confidence < 90 || f.AI.Confidence > 100 {
			return out, errors.New("AI proposal requires sufficient confidence and independent intent")
		}
		if err = checkServiceProposal(s.Service, c, proposal); err != nil {
			return out, err
		}
	}
	if reflect.DeepEqual(s.Service.Spec, c.Spec) {
		out.Status = "no-change"
		out.Prerequisites = []string{"Service routing already matches approved intent; investigate backend readiness and dependency health"}
		return out, nil
	}
	if !reflect.DeepEqual(s.Service.Spec.Selector, c.Spec.Selector) {
		out.ChangedFields = append(out.ChangedFields, "spec.selector")
	}
	if s.Service.Spec.Ports[index].TargetPort != c.Spec.Ports[index].TargetPort {
		out.ChangedFields = append(out.ChangedFields, fmt.Sprintf("spec.ports[%s:%d].targetPort", p.Spec.Port.Name, p.Spec.Port.Port))
	}
	edited := map[string]any{}
	if len(out.ChangedFields) > 0 {
		edited["selector"] = c.Spec.Selector
		edited["targetPort"] = scalarTargetPort(c.Spec.Ports[index].TargetPort)
	}
	preview, _ := yaml.Marshal(edited)
	out.Diff = string(preview)
	managed := f.GitOps.ManagedBy != "" || f.GitOps.TargetAdvice != "" || f.GitOps.HelmRelease != "" || f.GitOps.HelmChart != "" || f.GitOps.FluxHint != "" || f.GitOps.ArgoHint != "" || serviceManaged(s.Service)
	out.Status = "candidate-prepared"
	if managed {
		out.Status = "source-review-required"
	}
	proof := &Proof{authority: a, before: s, candidate: c, sourceOnly: managed, portIndex: index, reportBinding: digest(out)}
	proof.binding = proof.contentBinding()
	out.Proof = proof
	return out, nil
}
func serviceManaged(s *corev1.Service) bool {
	if s.Annotations["meta.helm.sh/release-name"] != "" || s.Labels["app.kubernetes.io/managed-by"] == "Helm" {
		return true
	}
	for _, ref := range s.OwnerReferences {
		if ref.Controller != nil && *ref.Controller {
			return true
		}
	}
	markers := map[string]string{}
	for k, v := range s.Labels {
		markers[k] = v
	}
	for k, v := range s.Annotations {
		markers[k] = v
	}
	for k := range markers {
		if strings.Contains(k, "argocd.argoproj.io") || strings.Contains(k, "toolkit.fluxcd.io") {
			return true
		}
	}
	return false
}
func checkServiceProposal(before, expected *corev1.Service, patch string) error {
	if len(patch) > maxPolicyBytes {
		return errors.New("Service proposal exceeds limit")
	}
	var n yaml.Node
	dec := yaml.NewDecoder(strings.NewReader(patch))
	if dec.Decode(&n) != nil || checkPolicyNode(&n) != nil {
		return errors.New("invalid Service proposal")
	}
	var extra yaml.Node
	if dec.Decode(&extra) != io.EOF {
		return errors.New("one Service proposal document required")
	}
	var obj map[string]any
	if yaml.Unmarshal([]byte(patch), &obj) != nil {
		return errors.New("invalid Service proposal")
	}
	meta, _ := obj["metadata"].(map[string]any)
	if obj["apiVersion"] != "v1" || obj["kind"] != "Service" || meta["name"] != before.Name || meta["namespace"] != before.Namespace {
		return errors.New("AI Service identity differs")
	}
	for k := range obj {
		if k != "apiVersion" && k != "kind" && k != "metadata" && k != "spec" {
			return errors.New("unsupported Service proposal field")
		}
	}
	for k := range meta {
		if k != "name" && k != "namespace" {
			return errors.New("Service metadata edits are not authorized")
		}
	}
	raw, err := syaml.YAMLToJSON([]byte(patch))
	if err != nil {
		return errors.New("invalid Service proposal")
	}
	base, _ := json.Marshal(before)
	merged, err := strategicpatch.StrategicMergePatch(base, raw, corev1.Service{})
	if err != nil {
		return errors.New("invalid Service merge")
	}
	var got corev1.Service
	if json.Unmarshal(merged, &got) != nil {
		return errors.New("invalid Service proposal result")
	}
	got.TypeMeta = expected.TypeMeta // envelope identity was independently validated above
	if !reflect.DeepEqual(&got, expected) {
		return errors.New("AI Service proposal differs from approved exact field change")
	}
	return nil
}

// PatchYAML is review data only and never grants runtime apply authority.
func (plan Plan) PatchYAML() (string, error) {
	p := plan.Proof
	if p == nil {
		return "", errors.New("concrete Service proof required")
	}
	if err := p.checkPlan(plan); err != nil {
		return "", err
	}
	if p.binding != p.contentBinding() {
		return "", errors.New("Service proof changed")
	}
	spec := map[string]any{}
	if !reflect.DeepEqual(p.before.Service.Spec.Selector, p.candidate.Spec.Selector) {
		selector := map[string]any{"$patch": "replace"}
		for k, v := range p.candidate.Spec.Selector {
			selector[k] = v
		}
		spec["selector"] = selector
	}
	old, next := p.before.Service.Spec.Ports[p.portIndex], p.candidate.Spec.Ports[p.portIndex]
	if old.TargetPort != next.TargetPort {
		spec["ports"] = []any{map[string]any{"name": next.Name, "port": next.Port, "protocol": next.Protocol, "targetPort": scalarTargetPort(next.TargetPort)}}
	}
	obj := map[string]any{"apiVersion": "v1", "kind": "Service", "metadata": map[string]any{"name": p.candidate.Name, "namespace": p.candidate.Namespace}, "spec": spec}
	raw, err := yaml.Marshal(obj)
	return string(raw), err
}

func scalarTargetPort(port intstr.IntOrString) any {
	if port.Type == intstr.String {
		return port.StrVal
	}
	return port.IntVal
}
