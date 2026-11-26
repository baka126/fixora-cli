package analyzer

import (
	"encoding/base64"
	"fmt"
	"sort"
	"strings"
)

const (
	maxSecretPodSpecs          = 5000
	maxSecretReferenceFindings = 1000
	maxSecretKeysInFinding     = 20
)

// secretPodSpec keeps source identity beside a PodSpec so a missing reference
// can be traced to a controller template even when it has no live Pod.
type secretPodSpec struct {
	namespace string
	kind      string
	name      string
	path      string
	spec      map[string]any
}

type secretInfo struct {
	keys       map[string]bool
	secretType string
}

type secretCoverageError struct {
	resource    string
	rbacBlocked bool
}

func (e secretCoverageError) Error() string {
	if e.rbacBlocked {
		return "read denied by RBAC for " + e.resource + "; Secret reference coverage is incomplete"
	}
	return "could not read " + e.resource + "; Secret reference coverage is incomplete"
}

func secretReadError(resource string, err error) error {
	return secretCoverageError{resource: resource, rbacBlocked: classifyReadError(err)}
}

type secretFindingSet struct {
	byID map[string]Finding
}

func (s *secretFindingSet) add(f Finding) error {
	if _, exists := s.byID[f.ID]; exists {
		return nil
	}
	if len(s.byID) >= maxSecretReferenceFindings {
		return fmt.Errorf("Secret analysis found more than %d distinct issues; narrow the namespace or selector", maxSecretReferenceFindings)
	}
	s.byID[f.ID] = f
	return nil
}

func (s *secretFindingSet) values() []Finding {
	ids := make([]string, 0, len(s.byID))
	for id := range s.byID {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]Finding, 0, len(ids))
	for _, id := range ids {
		out = append(out, s.byID[id])
	}
	return out
}

// analyzeSecrets checks Secret key presence and imagePullSecret schema. It
// examines encoded values only for the existing optional base64 check; values
// are discarded and never included in findings. Secret reads remain opt-in.
func (a Analyzer) analyzeSecrets(ctx *ScanContext) ([]Finding, error) {
	if !a.opts.CheckSecretKeys {
		return nil, nil
	}
	secrets, err := ctx.GetResourceItems(a.opts.Namespace, a.opts.AllNS, "secrets")
	if err != nil {
		return nil, secretReadError("secrets", err)
	}
	sources, err := secretPodSpecs(ctx)
	if err != nil {
		return nil, err
	}

	secretsByKey := make(map[string]secretInfo, len(secrets))
	for _, secret := range secrets {
		ns, name := objectNamespaceName(secret)
		info := secretInfo{keys: map[string]bool{}, secretType: strValue(secret["type"])}
		data, _ := secret["data"].(map[string]any)
		for key := range data {
			info.keys[key] = true
		}
		secretsByKey[keyFor(ns, name)] = info
	}

	findings := secretFindingSet{byID: map[string]Finding{}}
	// Keep the legacy check. The decoded bytes and encoded input are never retained.
	for _, secret := range secrets {
		ns, name := objectNamespaceName(secret)
		data, _ := secret["data"].(map[string]any)
		keys := make([]string, 0, len(data))
		for key := range data {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			_, decodeErr := base64.StdEncoding.DecodeString(strValue(data[key]))
			if decodeErr != nil {
				f := secretFindingID(ns, name, "SecretInvalidBase64", "medium", "data/"+key, key+": value is not valid base64", "Review the Secret data encoding at its source. Fixora never reports the value.")
				if err := findings.add(f); err != nil {
					return nil, err
				}
			}
		}
	}

	serviceAccounts := map[string]map[string]any{}
	for _, source := range sources {
		if err := a.checkPodSpecSecretRefs(ctx, source, secretsByKey, serviceAccounts, &findings); err != nil {
			return nil, err
		}
	}
	return findings.values(), nil
}

func secretPodSpecs(ctx *ScanContext) ([]secretPodSpec, error) {
	type templateSource struct{ resource, kind, path string }
	sources := []templateSource{
		{resource: "pods", kind: "Pod", path: "spec"},
		{resource: "deployments", kind: "Deployment", path: "spec.template.spec"},
		{resource: "statefulsets", kind: "StatefulSet", path: "spec.template.spec"},
		{resource: "daemonsets", kind: "DaemonSet", path: "spec.template.spec"},
		{resource: "replicasets", kind: "ReplicaSet", path: "spec.template.spec"},
		{resource: "replicationcontrollers", kind: "ReplicationController", path: "spec.template.spec"},
		{resource: "jobs", kind: "Job", path: "spec.template.spec"},
		{resource: "cronjobs", kind: "CronJob", path: "spec.jobTemplate.spec.template.spec"},
	}
	var out []secretPodSpec
	for _, definition := range sources {
		items, err := ctx.GetResourceItems(ctx.Opts.Namespace, ctx.Opts.AllNS, definition.resource)
		if err != nil {
			return nil, secretReadError(definition.resource, err)
		}
		for _, item := range items {
			ns, name := objectNamespaceName(item)
			spec, ok := secretNestedMapPath(item, definition.path)
			if !ok {
				return nil, fmt.Errorf("%s/%s has no readable PodSpec for Secret reference checks", definition.kind, name)
			}
			out = append(out, secretPodSpec{namespace: ns, kind: definition.kind, name: name, path: definition.path, spec: spec})
			if len(out) > maxSecretPodSpecs {
				return nil, fmt.Errorf("Secret reference analysis exceeds %d PodSpecs; narrow the namespace or selector", maxSecretPodSpecs)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.namespace != b.namespace {
			return a.namespace < b.namespace
		}
		if a.kind != b.kind {
			return a.kind < b.kind
		}
		return a.name < b.name
	})
	return out, nil
}

func secretNestedMapPath(object map[string]any, path string) (map[string]any, bool) {
	current := object
	for _, part := range strings.Split(path, ".") {
		value, ok := current[part].(map[string]any)
		if !ok {
			return nil, false
		}
		current = value
	}
	return current, true
}

func (a Analyzer) checkPodSpecSecretRefs(ctx *ScanContext, source secretPodSpec, secrets map[string]secretInfo, serviceAccounts map[string]map[string]any, findings *secretFindingSet) error {
	owner := source.kind + "/" + source.name
	addRef := func(secretName, path, key string, optional bool) error {
		if secretName == "" || optional {
			return nil
		}
		sourcePath := owner + "/" + source.path + "/" + path
		info, exists := secrets[keyFor(source.namespace, secretName)]
		if !exists {
			return findings.add(secretFindingID(source.namespace, secretName, "SecretMissingKey", "high", sourcePath, sourcePath+" references missing Secret "+secretName+keyEvidenceSuffix(key), "Create the referenced Secret or correct the reference in the workload source."))
		}
		if key != "" && !info.keys[key] {
			return findings.add(secretFindingID(source.namespace, secretName, "SecretMissingKey", "high", sourcePath+"/"+key, sourcePath+" needs Secret "+secretName+" key "+key+"; present keys: "+summarizeSecretKeys(info.keys), "Add the required key to the Secret or correct the reference in the workload source."))
		}
		return nil
	}
	addPullSecret := func(secretName, referenceSource string) error {
		if secretName == "" {
			return nil
		}
		info, exists := secrets[keyFor(source.namespace, secretName)]
		if !exists {
			return findings.add(secretFindingID(source.namespace, secretName, "MissingPullSecret", "medium", referenceSource, referenceSource+" references image-pull Secret "+secretName+", which was not found", "Create the image-pull Secret with the required type and key at the source of truth."))
		}
		expectedKey := ""
		switch info.secretType {
		case "kubernetes.io/dockerconfigjson":
			expectedKey = ".dockerconfigjson"
		case "kubernetes.io/dockercfg":
			expectedKey = ".dockercfg"
		default:
			return findings.add(secretFindingID(source.namespace, secretName, "MissingPullSecret", "medium", referenceSource, referenceSource+" references Secret "+secretName+" with type "+info.secretType+"; expected a Docker config Secret type", "Create or correct the image-pull Secret type at the source of truth."))
		}
		if !info.keys[expectedKey] {
			return findings.add(secretFindingID(source.namespace, secretName, "MissingPullSecret", "medium", referenceSource+"/"+expectedKey, referenceSource+" references Docker config Secret "+secretName+" missing required key "+expectedKey, "Add the required Docker config key at the source of truth. Fixora does not inspect its value."))
		}
		return nil
	}

	for _, containerField := range []string{"containers", "initContainers", "ephemeralContainers"} {
		for _, raw := range nestedSlice(source.spec, containerField) {
			container, _ := raw.(map[string]any)
			containerName := strValue(container["name"])
			containerPath := containerField + "/" + containerName
			for _, rawEnv := range nestedSlice(container, "env") {
				env, _ := rawEnv.(map[string]any)
				ref := nestedMap(nestedMap(env, "valueFrom"), "secretKeyRef")
				if err := addRef(strValue(ref["name"]), containerPath+"/env/"+strValue(env["name"]), strValue(ref["key"]), secretReferenceOptional(ref)); err != nil {
					return err
				}
			}
			for i, rawEnvFrom := range nestedSlice(container, "envFrom") {
				envFrom, _ := rawEnvFrom.(map[string]any)
				ref := nestedMap(envFrom, "secretRef")
				if err := addRef(strValue(ref["name"]), fmt.Sprintf("%s/envFrom/%d", containerPath, i), "", secretReferenceOptional(ref)); err != nil {
					return err
				}
			}
		}
	}

	for _, rawVolume := range nestedSlice(source.spec, "volumes") {
		volume, _ := rawVolume.(map[string]any)
		volumeName := strValue(volume["name"])
		volumePath := fmt.Sprintf("volumes/%s", volumeName)
		secretVolume := nestedMap(volume, "secret")
		if name := strValue(secretVolume["secretName"]); name != "" {
			if err := checkSecretVolumeItems(addRef, secrets, source.namespace, name, volumePath+"/secret", nestedSlice(secretVolume, "items"), boolValue(secretVolume["optional"])); err != nil {
				return err
			}
		}
		projected := nestedMap(volume, "projected")
		for sourceIndex, rawProjectedSource := range nestedSlice(projected, "sources") {
			projectedSource, _ := rawProjectedSource.(map[string]any)
			secretProjection := nestedMap(projectedSource, "secret")
			if name := strValue(secretProjection["name"]); name != "" {
				path := fmt.Sprintf("%s/projected/sources/%d/secret", volumePath, sourceIndex)
				if err := checkSecretVolumeItems(addRef, secrets, source.namespace, name, path, nestedSlice(secretProjection, "items"), boolValue(secretProjection["optional"])); err != nil {
					return err
				}
			}
		}
	}

	for i, rawPullSecret := range nestedSlice(source.spec, "imagePullSecrets") {
		pullSecret, _ := rawPullSecret.(map[string]any)
		referenceSource := fmt.Sprintf("%s/%s/imagePullSecrets/%d", owner, source.path, i)
		if err := addPullSecret(strValue(pullSecret["name"]), referenceSource); err != nil {
			return err
		}
	}

	serviceAccountName := strValue(source.spec["serviceAccountName"])
	if serviceAccountName == "" {
		serviceAccountName = "default"
	}
	saKey := keyFor(source.namespace, serviceAccountName)
	sa, cached := serviceAccounts[saKey]
	if !cached {
		var err error
		sa, err = ctx.GetResource(source.namespace, "serviceaccount/"+serviceAccountName)
		if err != nil {
			return secretReadError("serviceaccounts", err)
		}
		serviceAccounts[saKey] = sa
	}
	for i, rawPullSecret := range nestedSlice(sa, "imagePullSecrets") {
		pullSecret, _ := rawPullSecret.(map[string]any)
		referenceSource := fmt.Sprintf("ServiceAccount/%s/%s/imagePullSecrets/%d", source.namespace, serviceAccountName, i)
		if err := addPullSecret(strValue(pullSecret["name"]), referenceSource); err != nil {
			return err
		}
	}
	return nil
}

func checkSecretVolumeItems(addRef func(string, string, string, bool) error, secrets map[string]secretInfo, namespace, secretName, path string, items []any, optional bool) error {
	if optional {
		return nil
	}
	if len(items) == 0 {
		return addRef(secretName, path, "", optional)
	}
	if _, exists := secrets[keyFor(namespace, secretName)]; !exists {
		return addRef(secretName, path, "", false)
	}
	for i, rawItem := range items {
		item, _ := rawItem.(map[string]any)
		key := strValue(item["key"])
		if key == "" {
			continue
		}
		if err := addRef(secretName, fmt.Sprintf("%s/items/%d", path, i), key, optional); err != nil {
			return err
		}
	}
	return nil
}

func keyEvidenceSuffix(key string) string {
	if key == "" {
		return ""
	}
	return " (expected key " + key + ")"
}

func summarizeSecretKeys(keys map[string]bool) string {
	ordered := sortedKeys(keys)
	if len(ordered) > maxSecretKeysInFinding {
		return strings.Join(ordered[:maxSecretKeysInFinding], ", ") + fmt.Sprintf(", … (%d more)", len(ordered)-maxSecretKeysInFinding)
	}
	if len(ordered) == 0 {
		return "none"
	}
	return strings.Join(ordered, ", ")
}

func secretReferenceOptional(ref map[string]any) bool { return boolValue(ref["optional"]) }

func isImagePullSecretType(secretType string) bool {
	return secretType == "kubernetes.io/dockerconfigjson" || secretType == "kubernetes.io/dockercfg"
}

// secretFinding and secretFindingID preserve a safe Secret-only target; source
// paths are part of the finding ID so separate workload references are located.
func secretFinding(namespace, name, status, severity, evidence, recommendation string) Finding {
	return secretFindingID(namespace, name, status, severity, "", evidence, recommendation)
}

func secretFindingID(namespace, name, status, severity, discriminator, evidence, recommendation string) Finding {
	id := keyFor(namespace, "Secret/"+name+"/"+status)
	if discriminator != "" {
		id = keyFor(namespace, "Secret/"+name+"/"+status+"/"+discriminator)
	}
	return Finding{
		ID: id, Namespace: namespace, ResourceKind: "Secret", ResourceName: name, Status: status, Severity: severity, Category: "configuration", Summary: "Secret " + name + ": " + status,
		Evidence:        []Evidence{{Label: "Secret", Value: evidence}},
		Recommendations: []Recommendation{{Title: "Review Secret reference", Description: recommendation, PatchType: "secret", SafeByDefault: false}},
	}
}
