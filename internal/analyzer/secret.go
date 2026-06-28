package analyzer

import (
	"encoding/base64"
	"sort"
	"strings"
)

// analyzeSecrets checks Secret key presence, base64 validity, and imagePullSecret
// resolution. It reads ONLY key names and base64-decodability — never secret
// values. It is off by default; set Options.CheckSecretKeys = true to enable.
func (a Analyzer) analyzeSecrets(ctx *ScanContext) ([]Finding, error) {
	if !a.opts.CheckSecretKeys {
		return nil, nil
	}

	secrets, err := ctx.GetResourceItems(a.opts.Namespace, a.opts.AllNS, "secrets")
	if err != nil {
		return nil, err
	}
	pods, _ := ctx.GetResourceItems(a.opts.Namespace, a.opts.AllNS, "pods")

	// Build a map of secret name → set of data keys (never values).
	// Also track the secret type for imagePullSecret validation.
	type secretInfo struct {
		keys       map[string]bool
		secretType string
	}
	secretsByKey := map[string]*secretInfo{}
	for _, s := range secrets {
		ns, name := objectNamespaceName(s)
		k := keyFor(ns, name)
		info := &secretInfo{
			keys:       map[string]bool{},
			secretType: strValue(s["type"]),
		}
		data, _ := s["data"].(map[string]any)
		for key := range data {
			info.keys[key] = true
		}
		secretsByKey[k] = info
	}

	out := []Finding{}

	// Pass 1: base64 validity — check each key's value for decodability.
	// Decoded bytes are discarded immediately; values never surface in findings.
	for _, s := range secrets {
		ns, name := objectNamespaceName(s)
		data, _ := s["data"].(map[string]any)
		for key, val := range data {
			encoded := strValue(val)
			_, decErr := base64.StdEncoding.DecodeString(encoded)
			if decErr != nil {
				out = append(out, secretFinding(ns, name, "SecretInvalidBase64", "medium",
					key+": not valid base64",
					"Fixora reports key presence/schema, never secret values. Correct the base64 encoding of this key."))
			}
		}
	}

	// Pass 2: pod-referenced checks.
	for _, pod := range pods {
		podNS, podName := objectNamespaceName(pod)
		spec := nestedMap(pod, "spec")

		// secretKeyRef checks — key must exist in the named secret.
		for _, container := range podAllContainers(pod) {
			containerMap, _ := container.(map[string]any)
			for _, env := range nestedSlice(containerMap, "env") {
				envMap, _ := env.(map[string]any)
				ref := nestedMap(nestedMap(envMap, "valueFrom"), "secretKeyRef")
				secretName := strValue(ref["name"])
				keyName := strValue(ref["key"])
				if secretName == "" || keyName == "" {
					continue
				}
				sKey := keyFor(podNS, secretName)
				info, exists := secretsByKey[sKey]
				if !exists {
					// Secret itself is missing; imagePullSecret check covers pull secrets.
					continue
				}
				if !info.keys[keyName] {
					presentKeys := sortedKeys(info.keys)
					out = append(out, secretFinding(podNS, secretName, "SecretMissingKey", "high",
						podName+" needs key "+keyName+"; present keys: "+strings.Join(presentKeys, ", "),
						"Fixora reports key presence/schema, never secret values. Add the missing key to the Secret or update the workload reference."))
				}
			}
		}

		// imagePullSecrets checks — secret must exist and be of docker type.
		for _, ips := range nestedSlice(spec, "imagePullSecrets") {
			ipsMap, _ := ips.(map[string]any)
			secretName := strValue(ipsMap["name"])
			if secretName == "" {
				continue
			}
			sKey := keyFor(podNS, secretName)
			info, exists := secretsByKey[sKey]
			if !exists || info.secretType != "kubernetes.io/dockerconfigjson" {
				reason := podName + " references imagePullSecret " + secretName
				if !exists {
					reason += " (secret not found)"
				} else {
					reason += " (type is " + info.secretType + ", want kubernetes.io/dockerconfigjson)"
				}
				out = append(out, secretFinding(podNS, secretName, "MissingPullSecret", "medium",
					reason,
					"Fixora reports key presence/schema, never secret values. Create or fix the imagePullSecret with the correct type."))
			}
		}
	}

	return out, nil
}

func secretFinding(namespace, name, status, severity, evidence, recommendation string) Finding {
	return Finding{
		ID:           keyFor(namespace, "Secret/"+name+"/"+status),
		Namespace:    namespace,
		ResourceKind: "Secret",
		ResourceName: name,
		Status:       status,
		Severity:     severity,
		Category:     "configuration",
		Summary:      "Secret " + name + ": " + status,
		Evidence:     []Evidence{{Label: "Secret", Value: evidence}},
		Recommendations: []Recommendation{{
			Title:         "Review Secret key schema",
			Description:   recommendation,
			PatchType:     "secret",
			SafeByDefault: false,
		}},
	}
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
