package analyzer

import "fmt"

// analyzeResourceClaims reports missing DeviceClasses referenced by claim requests.
// Both APIs are optional: read failures are surfaced as skipped checks by the
// precision analyzer runner, and no automatic remediation is generated for these claims.
func (a Analyzer) analyzeResourceClaims(ctx *ScanContext) ([]Finding, error) {
	claims, err := ctx.GetResourceItems(a.opts.Namespace, a.opts.AllNS, "resourceclaims.resource.k8s.io")
	if err != nil || len(claims) == 0 {
		return nil, err
	}
	classes, err := ctx.Reader.GetResourceItems(ctx, "", false, "deviceclasses.resource.k8s.io")
	if err != nil {
		return nil, err
	}
	available := make(map[string]bool, len(classes))
	for _, class := range classes {
		_, name := objectNamespaceName(class)
		if name != "" {
			available[name] = true
		}
	}
	var out []Finding
	for _, claim := range claims {
		namespace, name := objectNamespaceName(claim)
		requests := nestedSlice(nestedMap(nestedMap(claim, "spec"), "devices"), "requests")
		reported := map[string]bool{}
		for _, raw := range requests {
			request, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			className := strValue(request["deviceClassName"])
			if className == "" || available[className] || reported[className] {
				continue
			}
			reported[className] = true
			out = append(out, Finding{
				ID:        keyFor(namespace, "ResourceClaim/"+name+"/DeviceClassNotFound/"+className),
				Namespace: namespace, ResourceKind: "ResourceClaim", ResourceName: name,
				Status: "DeviceClassNotFound", Severity: "high", Category: "scheduling",
				Summary:  fmt.Sprintf("ResourceClaim %s references missing DeviceClass %s", keyFor(namespace, name), className),
				Evidence: []Evidence{{Label: "DeviceClass", Value: className}}, GitOps: gitOpsForObject(claim),
				Recommendations: []Recommendation{{Title: "Review device class configuration", Description: "Verify that the DeviceClass exists and that the claim requests the intended class.", PatchType: "resourceclaim", SafeByDefault: false}},
			})
		}
	}
	return out, nil
}
