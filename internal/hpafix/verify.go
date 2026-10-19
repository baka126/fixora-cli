package hpafix

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync/atomic"
)

type Receipt struct{ state *receiptState }
type receiptState struct {
	consumed                                                       atomic.Bool
	planDigest, snapshotDigest, hpaUID, targetUID, hpaRV, targetRV string
}

func (r *Receipt) consume() error {
	if r == nil || r.state == nil {
		return errors.New("fresh private HPA verification receipt required")
	}
	if !r.state.consumed.CompareAndSwap(false, true) {
		return errors.New("HPA verification receipt already consumed")
	}
	return nil
}

func Verify(ctx context.Context, r Runtime, p Plan) (*Receipt, error) {
	if err := p.check(); err != nil {
		return nil, err
	}
	before, err := collectSnapshot(ctx, r, &p)
	if err != nil {
		return nil, err
	}
	patch, err := requestPatch(p, before.Workload)
	if err != nil {
		return nil, err
	}
	admitted, err := r.PatchWorkload(ctx, p.Namespace, p.TargetKind, p.TargetName, patch, true)
	if err != nil {
		return nil, fmt.Errorf("server-side dry-run rejected HPA candidate: %w", err)
	}
	if err = validateAdmitted(p, before.Workload, admitted); err != nil {
		return nil, err
	}
	if err = p.check(); err != nil {
		return nil, err
	}
	after, err := collectSnapshot(ctx, r, &p)
	if err != nil {
		return nil, err
	}
	if before.Digest != after.Digest {
		return nil, errors.New("HPA evidence changed during preflight; prepare again")
	}
	return &Receipt{state: &receiptState{planDigest: p.planDigest, snapshotDigest: before.Digest, hpaUID: string(before.HPA.UID), targetUID: strMap(nestedMap(before.Workload, "metadata")["uid"]), hpaRV: before.HPA.ResourceVersion, targetRV: strMap(nestedMap(before.Workload, "metadata")["resourceVersion"])}}, nil
}

type patchOperation struct {
	Op    string `json:"op"`
	Path  string `json:"path"`
	Value any    `json:"value,omitempty"`
}

func requestPatch(p Plan, current map[string]any) ([]byte, error) {
	meta := nestedMap(current, "metadata")
	ops := []patchOperation{{Op: "test", Path: "/metadata/uid", Value: meta["uid"]}, {Op: "test", Path: "/metadata/resourceVersion", Value: meta["resourceVersion"]}}
	containers, ok := nestedMap(current, "spec", "template", "spec")["containers"].([]any)
	if !ok {
		return nil, errors.New("target regular containers are unavailable")
	}
	indices := map[string]int{}
	for i, raw := range containers {
		c, ok := raw.(map[string]any)
		if !ok {
			return nil, errors.New("malformed target container")
		}
		indices[strMap(c["name"])] = i
	}
	addedResource := map[int]bool{}
	addedRequests := map[int]bool{}
	for _, changed := range p.ChangedPaths {
		start := strings.Index(changed, "containers[name=")
		if start < 0 {
			return nil, errors.New("invalid private HPA changed path")
		}
		rest := changed[start+len("containers[name="):]
		end := strings.Index(rest, "]")
		if end < 0 {
			return nil, errors.New("invalid private HPA container path")
		}
		name := rest[:end]
		idx, ok := indices[name]
		if !ok {
			return nil, errors.New("HPA target container changed")
		}
		resourceName := rest[end+1:]
		resourceName = strings.TrimPrefix(resourceName, ".resources.requests.")
		if resourceName != "cpu" && resourceName != "memory" {
			return nil, errors.New("unsupported HPA request path")
		}
		path := fmt.Sprintf("/spec/template/spec/containers/%d", idx)
		orig := containers[idx].(map[string]any)
		resources, hasResources := orig["resources"].(map[string]any)
		if !hasResources {
			if !addedResource[idx] {
				value := nestedMap(p.candidate, "spec", "template", "spec")["containers"].([]any)[idx].(map[string]any)["resources"]
				ops = append(ops, patchOperation{Op: "add", Path: path + "/resources", Value: value})
				addedResource[idx] = true
			}
			continue
		}
		requests, hasRequests := resources["requests"].(map[string]any)
		if !hasRequests {
			if !addedRequests[idx] {
				value := nestedMap(p.candidate, "spec", "template", "spec")["containers"].([]any)[idx].(map[string]any)["resources"].(map[string]any)["requests"]
				ops = append(ops, patchOperation{Op: "add", Path: path + "/resources/requests", Value: value})
				addedRequests[idx] = true
			}
			continue
		}
		if _, exists := requests[resourceName]; exists {
			return nil, errors.New("approved HPA request field is no longer missing")
		}
		value := nestedMap(p.candidate, "spec", "template", "spec")["containers"].([]any)[idx].(map[string]any)["resources"].(map[string]any)["requests"].(map[string]any)[resourceName]
		ops = append(ops, patchOperation{Op: "add", Path: path + "/resources/requests/" + resourceName, Value: value})
	}
	return json.Marshal(ops)
}

func validateAdmitted(p Plan, before, admitted map[string]any) error {
	if admitted == nil {
		return errors.New("dry-run returned no target object")
	}
	meta := nestedMap(admitted, "metadata")
	old := nestedMap(before, "metadata")
	if strMap(meta["uid"]) != strMap(old["uid"]) || strMap(meta["name"]) != p.TargetName || strMap(meta["namespace"]) != p.Namespace || admitted["apiVersion"] != before["apiVersion"] || admitted["kind"] != before["kind"] {
		return errors.New("dry-run returned a replaced or mismatched target")
	}
	if !sameUserMetadata(old, meta) {
		return errors.New("admission changed target metadata outside the approved HPA request patch")
	}
	if !reflect.DeepEqual(nestedMap(admitted, "spec"), nestedMap(p.candidate, "spec")) {
		return errors.New("admission changed fields outside the approved HPA request candidate")
	}
	return nil
}

func sameUserMetadata(before, after map[string]any) bool {
	clone := func(in map[string]any) map[string]any {
		out := map[string]any{}
		for key, value := range in {
			if key != "resourceVersion" && key != "generation" && key != "managedFields" {
				out[key] = value
			}
		}
		return out
	}
	return reflect.DeepEqual(clone(before), clone(after))
}

var _ = json.Valid
