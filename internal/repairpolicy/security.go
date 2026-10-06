package repairpolicy

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"

	"github.com/fixora/kubectl-fixora/internal/kube"
)

func CollectSecurity(ctx context.Context, r kube.Reader, s Snapshot) (Snapshot, error) {
	namespace, err := r.GetResource(ctx, "", "namespace/"+s.Target.Namespace)
	if err != nil {
		return s, err
	}
	s.Namespace = copyObject(namespace)
	events, err := r.GetEvents(ctx, s.Target.Namespace, "")
	if err != nil {
		return s, err
	}
	failure, err := kube.CurrentAdmissionFailure(ctx, r, s.Object, events)
	if err != nil {
		return s, err
	}
	if failure == nil {
		return s, errors.New("no current revision-linked Pod Security admission failure")
	}
	s.Revision = copyObject(failure.Revision)
	s.Events = append([]kube.Event(nil), failure.Events...)
	return s, nil
}
func Security(s Snapshot, a *Authority, patch string) (*Proof, error) {
	if _, err := snapshotBinding(s); err != nil {
		return nil, err
	}
	if a == nil || a.digest != digest(a.object) || a.digest == "" {
		return nil, errors.New("security repair requires explicit baseline authority")
	}
	if err := identityMatches(a.object, s.Target); err != nil {
		return nil, err
	}
	if err := securityNamespace(s.Namespace, s.Target.Namespace); err != nil {
		return nil, err
	}
	if !kube.CurrentAdmissionCondition(s.Revision) {
		return nil, errors.New("current admission-denied revision is not proven")
	}
	source, _ := kube.WorkloadPodSpec(s.Object)
	revision, ok := kube.WorkloadPodSpec(s.Revision)
	if !ok || !reflect.DeepEqual(copyValue(source), copyValue(revision)) {
		return nil, errors.New("admission failure does not belong to the current desired template")
	}
	meta := mapAny(s.Revision["metadata"])
	revisionUID, _ := meta["uid"].(string)
	revisionKind, _ := s.Revision["kind"].(string)
	revisionName, _ := meta["name"].(string)
	if meta["namespace"] != s.Target.Namespace {
		return nil, errors.New("denied revision namespace mismatch")
	}
	if s.Target.Kind == "Deployment" {
		refs, _ := meta["ownerReferences"].([]any)
		controllers := 0
		owned := false
		for _, v := range refs {
			ref := mapAny(v)
			if ref["controller"] == true {
				controllers++
				owned = ref["apiVersion"] == "apps/v1" && ref["kind"] == s.Target.Kind && ref["name"] == s.Target.Name && ref["uid"] == s.Target.UID
			}
		}
		if controllers != 1 || !owned || revisionKind != "ReplicaSet" {
			return nil, errors.New("admission revision controlling owner mismatch")
		}
	} else if revisionUID != s.Target.UID || revisionName != s.Target.Name || revisionKind != s.Target.Kind {
		return nil, errors.New("denied revision identity mismatch")
	}
	message := ""
	conditions, _ := mapAny(s.Revision["status"])["conditions"].([]any)
	for _, v := range conditions {
		c := mapAny(v)
		if c["type"] == "ReplicaFailure" && c["status"] == "True" && c["reason"] == "FailedCreate" {
			message, _ = c["message"].(string)
		}
	}
	eventMessage := ""
	for _, event := range s.Events {
		ref := event.InvolvedObject
		if event.Reason == "FailedCreate" && ref.UID == revisionUID && ref.Kind == revisionKind && ref.Name == revisionName && ref.Namespace == s.Target.Namespace {
			eventMessage += event.Message + " "
		}
	}
	if !strings.Contains(strings.ToLower(message), "podsecurity") || !strings.Contains(strings.ToLower(eventMessage), "podsecurity") {
		return nil, errors.New("current UID-linked admission event is required")
	}
	if os := mapAny(source["os"])["name"]; os != nil && os != "linux" {
		return nil, errors.New("only Linux security semantics are supported")
	}
	if mapAny(source["nodeSelector"])["kubernetes.io/os"] == "windows" {
		return nil, errors.New("Windows identity semantics require review")
	}
	if err := securitySource(source); err != nil {
		return nil, err
	}
	_, changes, err := policyPatch(patch, s.Target)
	if err != nil {
		return nil, err
	}
	fields := []string{}
	identities := securityIdentityBindings(source)
	containers, err := containerMap(source)
	if err != nil {
		return nil, err
	}

	for key, value := range changes {
		switch key {
		case "securityContext":
			sc, ok := value.(map[string]any)
			if !ok || len(sc) == 0 {
				return nil, errors.New("security context must be a nonempty object")
			}
			for field, v := range sc {
				if field != "runAsNonRoot" && field != "seccompProfile" {
					return nil, errors.New("pod security policy may change only runAsNonRoot or RuntimeDefault seccomp")
				}
				if field == "runAsNonRoot" {
					for _, section := range []string{"containers", "initContainers"} {
						values, _ := source[section].([]any)
						for _, raw := range values {
							if effectiveUID(source, mapAny(raw)) <= 0 {
								return nil, errors.New("runAsNonRoot requires an existing explicit positive UID for every affected container")
							}
						}
					}

				}
				if err = securityLeaf(a, []string{"securityContext", field}, field, v, message, eventMessage); err != nil {
					return nil, err
				}
				fields = append(fields, "securityContext/"+field)
			}
		case "containers":
			list, ok := value.([]any)
			if !ok || len(list) == 0 {
				return nil, errors.New("security candidate containers are malformed")
			}
			seen := map[string]bool{}
			for _, v := range list {
				c := mapAny(v)
				name, _ := c["name"].(string)
				if name == "" || containers[name] == nil || seen[name] || len(c) != 2 {
					return nil, errors.New("security candidate must retain unique container identity")
				}
				seen[name] = true
				sc, ok := c["securityContext"].(map[string]any)
				if !ok || len(sc) == 0 {
					return nil, errors.New("container security changes are missing")
				}
				for field, v := range sc {
					if field == "runAsNonRoot" && effectiveUID(source, containers[name]) <= 0 {
						return nil, errors.New("runAsNonRoot requires an existing explicit positive UID")
					}
					if err = securityLeaf(a, []string{"containers", name, "securityContext", field}, field, v, message, eventMessage); err != nil {
						return nil, err
					}
					fields = append(fields, "containers/"+name+"/securityContext/"+field)
				}
			}
		default:
			return nil, errors.New("security strengthening must not change application, storage or placement fields")
		}
	}
	if len(fields) == 0 {
		return nil, errors.New("security candidate has no strengthening fields")
	}
	p, err := bind(s, patch, "security-strengthen", a, fields)
	if err != nil {
		return nil, err
	}
	p.namespace = digest(s.Namespace)
	p.revision = securityRevisionBinding(s.Revision)
	p.identities = identities
	return p, nil
}
func securityNamespace(obj map[string]any, ns string) error {
	meta := mapAny(obj["metadata"])
	labels := mapAny(meta["labels"])
	if obj["kind"] != "Namespace" || meta["name"] != ns || meta["uid"] == nil || meta["uid"] == "" || labels["pod-security.kubernetes.io/enforce"] != "restricted" {
		return errors.New("readable enforced restricted Pod Security Admission policy is required")
	}
	version, _ := labels["pod-security.kubernetes.io/enforce-version"].(string)
	minor, err := strconv.Atoi(strings.TrimPrefix(version, "v1."))
	if err != nil || !strings.HasPrefix(version, "v1.") || minor < 25 || minor > 34 {
		return errors.New("pin enforced PSA to a supported version v1.25 through v1.34; unknown/latest policy requires review")
	}
	return nil
}
func securityLeaf(a *Authority, path []string, field string, v any, message, event string) error {
	allowed := false
	needle := field
	switch field {
	case "runAsNonRoot":
		allowed = v == true
	case "allowPrivilegeEscalation":
		allowed = v == false
	case "seccompProfile":
		sc := mapAny(v)
		allowed = len(sc) == 1 && sc["type"] == "RuntimeDefault"
	case "capabilities":
		caps := mapAny(v)
		drop, ok := stringArray(caps["drop"])
		allowed = len(caps) == 1 && ok && len(drop) == 1 && drop[0] == "ALL"
	default:
		return fmt.Errorf("security field %s requires source review", field)
	}
	if !allowed || !a.authorizes(path, v) {
		return fmt.Errorf("security field %s must exactly match approved strengthening", field)
	}
	if !strings.Contains(strings.ToLower(message), strings.ToLower(needle)) || !strings.Contains(strings.ToLower(event), strings.ToLower(needle)) {
		return fmt.Errorf("no field-specific current admission proof for %s", field)
	}
	return nil
}
func effectiveUID(spec, c map[string]any) float64 {
	value := mapAny(c["securityContext"])["runAsUser"]
	if value == nil {
		value = mapAny(spec["securityContext"])["runAsUser"]
	}
	uid, ok := copyValue(value).(float64)
	if !ok {
		return 0
	}
	return uid
}
func securitySource(spec map[string]any) error {
	for _, key := range []string{"hostNetwork", "hostPID", "hostIPC"} {
		if spec[key] == true {
			return errors.New("host namespace source remains blocked")
		}
	}
	if sa := spec["serviceAccountName"]; sa != nil && sa != "" && sa != "default" {
		return errors.New("nondefault service account source remains blocked")
	}
	if secretReference(spec) {
		return errors.New("Secret-bearing source remains blocked")
	}
	volumes, _ := spec["volumes"].([]any)
	for _, v := range volumes {
		volume := mapAny(v)
		if volume["persistentVolumeClaim"] != nil || volume["hostPath"] != nil {
			return errors.New("persistent or host storage source remains blocked")
		}
	}
	for _, section := range []string{"containers", "initContainers"} {
		cs, _ := spec[section].([]any)
		for _, v := range cs {
			sc := mapAny(mapAny(v)["securityContext"])
			if sc["privileged"] == true || sc["windowsOptions"] != nil {
				return errors.New("privileged or Windows source remains blocked")
			}
			caps := mapAny(sc["capabilities"])
			add, _ := caps["add"].([]any)
			if len(add) > 0 {
				return errors.New("elevated capability source remains blocked")
			}
		}
	}
	return nil
}
func VerifySecurity(ctx context.Context, r kube.Reader, p *Proof, pod *kube.Pod, patch string) (*Receipt, error) {
	if p == nil || p.family != "security-strengthen" || p.authority == "" || pod == nil {
		return nil, errors.New("bound security proof is required")
	}
	namespace, err := r.GetResource(ctx, "", "namespace/"+p.target.Namespace)
	if err != nil {
		return nil, err
	}
	if digest(namespace) != p.namespace {
		return nil, errors.New("namespace security policy changed")
	}
	observed := copyValue(pod.Spec).(map[string]any)
	if !reflect.DeepEqual(securityIdentityBindings(observed), p.identities) {
		return nil, errors.New("admission changed application UID/GID or storage groups")
	}

	return witness(ctx, r, p, pod, patch)
}

func securityIdentityBindings(spec map[string]any) map[string]string {
	out := map[string]string{}
	pod := mapAny(spec["securityContext"])
	for _, section := range []string{"containers", "initContainers"} {
		cs, _ := spec[section].([]any)
		for _, v := range cs {
			c := mapAny(v)
			name, _ := c["name"].(string)
			sc := mapAny(c["securityContext"])
			uid, gid := sc["runAsUser"], sc["runAsGroup"]
			if uid == nil {
				uid = pod["runAsUser"]
			}
			if gid == nil {
				gid = pod["runAsGroup"]
			}
			out[section+"/"+name] = digest([]any{uid, gid, pod["fsGroup"], pod["supplementalGroups"], pod["supplementalGroupsPolicy"]})
		}
	}
	return out
}

func securityRevisionBinding(obj map[string]any) string {
	meta := mapAny(obj["metadata"])
	spec, _ := kube.WorkloadPodSpec(obj)
	return digest([]any{meta["uid"], meta["generation"], spec})
}
