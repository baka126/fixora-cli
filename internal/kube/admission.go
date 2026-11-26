package kube

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
)

type AdmissionFailure struct {
	Revision map[string]any
	Events   []Event
	Message  string
}

// CurrentAdmissionFailure requires both a current FailedCreate condition and an
// event for that exact owner UID. Historical events alone are not incidents.
func CurrentAdmissionFailure(ctx context.Context, r Reader, target map[string]any, events []Event) (*AdmissionFailure, error) {
	kind, _ := target["kind"].(string)
	meta, _ := target["metadata"].(map[string]any)
	ns, _ := meta["namespace"].(string)
	uid, _ := meta["uid"].(string)
	name, _ := meta["name"].(string)
	if uid == "" {
		return nil, nil
	}
	hasAdmissionEvent := false
	for _, event := range events {
		if event.Reason == "FailedCreate" && strings.Contains(strings.ToLower(event.Message), "podsecurity") {
			hasAdmissionEvent = true
		}
	}
	if !hasAdmissionEvent {
		return nil, nil
	}
	candidates := []map[string]any{target}
	if kind == "Deployment" {
		items, err := r.GetResourceItems(ctx, ns, false, "replicasets")
		if err != nil {
			return nil, err
		}
		candidates = nil
		for _, item := range items {
			m, _ := item["metadata"].(map[string]any)
			refs, _ := m["ownerReferences"].([]any)
			owned := false
			controllers := 0
			for _, raw := range refs {
				ref, _ := raw.(map[string]any)
				if ref["controller"] == true {
					controllers++
					owned = ref["kind"] == kind && ref["apiVersion"] == "apps/v1" && ref["name"] == name && ref["uid"] == uid
				}
			}
			if controllers != 1 || !owned || m["namespace"] != ns || item["kind"] != "ReplicaSet" || item["apiVersion"] != "apps/v1" || !sameAdmissionTemplate(target, item) {
				continue
			}
			candidates = append(candidates, item)
		}
	}
	var out *AdmissionFailure
	for _, obj := range candidates {
		if !CurrentAdmissionCondition(obj) {
			continue
		}
		m, _ := obj["metadata"].(map[string]any)
		objUID, _ := m["uid"].(string)
		objName, _ := m["name"].(string)
		objKind, _ := obj["kind"].(string)
		status, _ := obj["status"].(map[string]any)
		conditions, _ := status["conditions"].([]any)
		for _, raw := range conditions {
			c, _ := raw.(map[string]any)
			message, _ := c["message"].(string)
			if c["type"] != "ReplicaFailure" || c["reason"] != "FailedCreate" || c["status"] != "True" || !strings.Contains(strings.ToLower(message), "podsecurity") {
				continue
			}
			matched := []Event{}
			for _, event := range events {
				ref := event.InvolvedObject
				if ref.UID == objUID && ref.Name == objName && ref.Kind == objKind && ref.Namespace == ns && event.Reason == "FailedCreate" && strings.Contains(strings.ToLower(event.Message), "podsecurity") {
					matched = append(matched, event)
				}
			}
			if len(matched) == 0 {
				continue
			}
			if out != nil {
				return nil, errors.New("multiple current denied revisions require source review")
			}
			out = &AdmissionFailure{Revision: obj, Events: matched, Message: message}
		}
	}
	return out, nil
}
func CurrentAdmissionCondition(obj map[string]any) bool {
	meta, _ := obj["metadata"].(map[string]any)
	status, _ := obj["status"].(map[string]any)
	spec, _ := obj["spec"].(map[string]any)
	generation := admissionNumber(meta["generation"])
	desired := admissionNumber(spec["replicas"])
	if spec["replicas"] == nil {
		desired = 1
	}
	return meta["uid"] != "" && generation > 0 && admissionNumber(status["observedGeneration"]) >= generation && desired > 0 && admissionNumber(status["readyReplicas"]) < desired
}
func sameAdmissionTemplate(a, b map[string]any) bool {
	as, aok := WorkloadPodSpec(a)
	bs, bok := WorkloadPodSpec(b)
	if !aok || !bok || !reflect.DeepEqual(as, bs) {
		return false
	}
	templateMeta := func(obj map[string]any) map[string]any {
		spec, _ := obj["spec"].(map[string]any)
		template, _ := spec["template"].(map[string]any)
		meta, _ := template["metadata"].(map[string]any)
		raw, _ := json.Marshal(meta)
		copy := map[string]any{}
		_ = json.Unmarshal(raw, &copy)
		if labels, ok := copy["labels"].(map[string]any); ok {
			delete(labels, "pod-template-hash")
		}
		if len(copy) == 0 {
			return nil
		}
		return copy
	}
	return reflect.DeepEqual(templateMeta(a), templateMeta(b))
}
func admissionNumber(v any) int64 {
	switch n := v.(type) {
	case float64:
		return int64(n)
	case int64:
		return n
	case int:
		return int64(n)
	}
	return 0
}
