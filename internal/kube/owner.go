package kube

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

type OwnerIdentity struct{ Kind, Name, UID string }

type WorkloadOwner struct {
	Identities      []OwnerIdentity
	Kind, Name, UID string
	Object          map[string]any
	Chain           []string
}

// ResolveWorkloadOwner follows only verified controlling references, never
// label selectors or generated-name prefixes. UID checks reject replaced owners.
func ResolveWorkloadOwner(ctx context.Context, r Reader, pod Pod) (WorkloadOwner, error) {
	out := WorkloadOwner{Kind: "Pod", Name: pod.Metadata.Name, UID: pod.Metadata.UID, Chain: []string{"Pod/" + pod.Metadata.Name}}
	meta := pod.Metadata
	kind := "Pod"
	seen := map[string]bool{}
	for depth := 0; depth < 8; depth++ {
		var owner *OwnerReference
		for i := range meta.OwnerRefs {
			ref := &meta.OwnerRefs[i]
			if ref.Controller == nil || !*ref.Controller {
				continue
			}
			if owner != nil {
				return out, fmt.Errorf("multiple controlling owners")
			}
			owner = ref
		}
		if owner == nil {
			return out, nil
		}
		if owner.UID == "" || owner.Name == "" || owner.APIVersion == "" || owner.APIVersion != WorkloadVersion(owner.Kind) {
			return out, fmt.Errorf("owner reference identity is incomplete")
		}
		key := strings.ToLower(owner.Kind) + "/" + owner.Name
		if seen[key] {
			return out, fmt.Errorf("owner cycle detected")
		}
		seen[key] = true
		if !supportedOwnerEdge(kind, owner.Kind) {
			return out, fmt.Errorf("unsupported controlling owner %s", key)
		}
		obj, err := r.GetResource(ctx, pod.Metadata.Namespace, key)
		if err != nil {
			return out, fmt.Errorf("read owner %s: %w", key, err)
		}
		raw, err := json.Marshal(obj)
		if err != nil {
			return out, err
		}
		var identity struct {
			Metadata ObjectMeta `json:"metadata"`
		}
		if err = json.Unmarshal(raw, &identity); err != nil {
			return out, err
		}
		meta = identity.Metadata
		if obj["kind"] != owner.Kind || obj["apiVersion"] != owner.APIVersion || meta.Name != owner.Name || meta.Namespace != pod.Metadata.Namespace || meta.UID != owner.UID {
			return out, fmt.Errorf("owner %s identity mismatch", key)
		}
		kind = owner.Kind
		out.Kind, out.Name, out.UID, out.Object = kind, meta.Name, meta.UID, obj
		out.Chain = append(out.Chain, kind+"/"+meta.Name)
		out.Identities = append(out.Identities, OwnerIdentity{Kind: kind, Name: meta.Name, UID: meta.UID})
	}
	return out, fmt.Errorf("owner traversal limit exceeded")
}
func supportedOwnerEdge(child, parent string) bool {
	if child == "Pod" {
		return parent == "ReplicaSet" || parent == "Job" || parent == "StatefulSet" || parent == "DaemonSet"
	}
	return child == "ReplicaSet" && parent == "Deployment" || child == "Job" && parent == "CronJob"
}

func (o WorkloadOwner) Matches(kind, name, uid string) bool {
	if uid == "" {
		return false
	}
	for _, i := range o.Identities {
		if i.Kind == kind && i.Name == name && i.UID == uid {
			return true
		}
	}
	return false
}
