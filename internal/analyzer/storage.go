package analyzer

import (
	"fmt"
	"sort"
	"strings"
)

func (a Analyzer) analyzeStorage(ctx *ScanContext) ([]Finding, error) {
	out := []Finding{}
	pvs, pvErr := ctx.GetResourceItems("", true, "pv")
	if pvErr == nil {
		for _, pv := range pvs {
			_, name := objectNamespaceName(pv)
			phase := strValue(nestedMap(pv, "status")["phase"])
			if phase == "Released" || phase == "Failed" {
				out = append(out, Finding{
					ID:           "cluster/PersistentVolume/" + name + "/" + phase,
					ResourceKind: "PersistentVolume",
					ResourceName: name,
					Status:       phase,
					Severity:     "medium",
					Category:     "storage",
					Summary:      "PersistentVolume is not available for healthy binding.",
					Evidence:     []Evidence{{Label: "Phase", Value: phase}},
					GitOps:       gitOpsForObject(pv),
					Recommendations: []Recommendation{{
						Title:         "Review reclaim and binding state",
						Description:   "Check the claimRef, reclaim policy, provisioner events, and data-retention requirements before deleting or recycling storage.",
						PatchType:     "storage",
						SafeByDefault: false,
					}},
				})
			}
		}
	}
	storageClasses, scErr := ctx.GetResourceItems("", true, "storageclasses")
	if scErr == nil {
		defaults := []string{}
		for _, sc := range storageClasses {
			_, name := objectNamespaceName(sc)
			_, annotations := objectLabelsAnnotations(sc)
			if annotations["storageclass.kubernetes.io/is-default-class"] == "true" || annotations["storageclass.beta.kubernetes.io/is-default-class"] == "true" {
				defaults = append(defaults, name)
			}
		}
		sort.Strings(defaults)
		if len(defaults) > 1 {
			out = append(out, Finding{
				ID:           "cluster/StorageClass/MultipleDefaults",
				ResourceKind: "StorageClass",
				ResourceName: strings.Join(defaults, ","),
				Status:       "MultipleDefaultStorageClasses",
				Severity:     "medium",
				Category:     "storage",
				Summary:      "Cluster has multiple default StorageClasses.",
				Evidence:     []Evidence{{Label: "Default StorageClasses", Value: strings.Join(defaults, ", ")}},
				Recommendations: []Recommendation{{
					Title:         "Keep one default StorageClass",
					Description:   "Choose a single default class to avoid surprising PVC provisioning behavior across namespaces.",
					PatchType:     "storage",
					SafeByDefault: false,
				}},
			})
		}
	}
	if pvErr != nil && scErr != nil {
		return nil, fmt.Errorf("pv: %v; storageclasses: %v", pvErr, scErr)
	}
	return out, nil
}
