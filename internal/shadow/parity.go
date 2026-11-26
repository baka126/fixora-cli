package shadow

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/fixora/kubectl-fixora/internal/analyzer"
	corev1 "k8s.io/api/core/v1"
)

func parityScore(original, clone *corev1.Pod) int {
	total := 0
	score := 0
	add := func(weight int, ok bool) {
		total += weight
		if ok {
			score += weight
		}
	}
	add(20, sameJSON(original.Spec.Containers, clone.Spec.Containers))
	add(12, sameJSON(original.Spec.InitContainers, clone.Spec.InitContainers))
	add(10, sameJSON(original.Spec.Volumes, clone.Spec.Volumes))
	add(8, original.Spec.ServiceAccountName == clone.Spec.ServiceAccountName)
	add(8, sameJSON(original.Spec.NodeSelector, clone.Spec.NodeSelector))
	add(8, sameJSON(original.Spec.Tolerations, clone.Spec.Tolerations))
	add(8, sameJSON(original.Spec.Affinity, clone.Spec.Affinity))
	add(8, sameJSON(original.Spec.SecurityContext, clone.Spec.SecurityContext))
	add(6, sameJSON(original.Spec.ImagePullSecrets, clone.Spec.ImagePullSecrets))
	add(6, original.Spec.RestartPolicy == clone.Spec.RestartPolicy)
	add(6, original.Spec.DNSPolicy == clone.Spec.DNSPolicy)
	if total == 0 {
		return 0
	}
	return score * 100 / total
}

func findingHasUnverifiedDependency(finding analyzer.Finding) bool {
	var evidence strings.Builder
	evidence.WriteString(finding.Status)
	for _, item := range finding.Evidence {
		evidence.WriteByte(' ')
		evidence.WriteString(item.Label)
		evidence.WriteByte(' ')
		evidence.WriteString(item.Value)
	}
	text := strings.ToLower(evidence.String())
	for _, signal := range []string{"databaseunreachable", "dependencyunreachable", "serviceunreachable", "connectionrefused", "dnsresolutionfailure", "database dependency"} {
		if strings.Contains(text, signal) {
			return true
		}
	}
	return false
}

func assessOperationalParity(evidence *FailureEvidence, module EvidenceModuleResult, dependencyNotExercised, productionTrafficExercised bool) ParityAssessment {
	assessment := ParityAssessment{Score: 100, Deductions: []ParityDeduction{}, Limitations: []string{}}
	addDeduction := func(category string, points int, summary string) {
		assessment.Deductions = append(assessment.Deductions, ParityDeduction{Category: category, Points: points, Summary: summary})
		assessment.Score -= points
	}
	if evidence != nil {
		seen := map[string]bool{}
		unknownReference := false
		for _, ref := range evidence.ConfigMaps {
			if ref.Optional {
				continue
			}
			identity := strings.TrimSpace(ref.Name) + "\x00" + strings.TrimSpace(ref.Key)
			if identity == "\x00" || seen[identity] {
				continue
			}
			seen[identity] = true
			if ref.SourceStatus == "unknown" || ref.SandboxStatus == "unknown" {
				unknownReference = true
				continue
			}
			if ref.SourceStatus != "key-present" || ref.SandboxStatus != "key-present" {
				addDeduction("unavailable-config", 15, "a required ConfigMap key was unavailable in the shadow environment")
			}
		}
		if unknownReference {
			assessment.Limitations = append(assessment.Limitations, "one or more required ConfigMap references could not be inspected")
		}
		secretSeen := map[string]bool{}
		for _, ref := range evidence.Secrets {
			if ref.Optional {
				continue
			}
			identity := strings.TrimSpace(ref.Name) + "\x00" + strings.TrimSpace(ref.Key)
			if identity == "\x00" || secretSeen[identity] {
				continue
			}
			secretSeen[identity] = true
			if ref.Status == "unknown" {
				assessment.Limitations = append(assessment.Limitations, "one or more required Secret references could not be inspected")
				continue
			}
			if ref.Status != "available-in-shadow" {
				addDeduction("unavailable-secret", 15, "a required Secret reference was not present in the shadow environment")
			}
		}
	}
	if dependencyNotExercised {
		addDeduction("unverified-dependency", 15, "a required service or database dependency was not exercised")
	} else {
		assessment.Limitations = append(assessment.Limitations, "required service and database reachability is not inferred from this isolated run")
	}
	if module.Status == EvidenceModuleUnsupported || module.Status == EvidenceModuleInconclusive {
		summary := "no failure-specific evidence module is available for this finding"
		if module.Status == EvidenceModuleInconclusive {
			summary = "failure-specific evidence could not be completed for this finding"
		}
		addDeduction("missing-evidence-module", 10, summary)
	}
	if !productionTrafficExercised {
		addDeduction("no-production-traffic", 10, "the isolated candidate received no production traffic")
	}
	if assessment.Score < 0 {
		assessment.Score = 0
	}
	sort.SliceStable(assessment.Deductions, func(i, j int) bool {
		if assessment.Deductions[i].Category != assessment.Deductions[j].Category {
			return assessment.Deductions[i].Category < assessment.Deductions[j].Category
		}
		return assessment.Deductions[i].Summary < assessment.Deductions[j].Summary
	})
	sort.Strings(assessment.Limitations)
	assessment.Limitations = compactStrings(assessment.Limitations)
	return assessment
}

func compactStrings(values []string) []string {
	if len(values) < 2 {
		return values
	}
	result := values[:1]
	for _, value := range values[1:] {
		if value != result[len(result)-1] {
			result = append(result, value)
		}
	}
	return result
}

func ParityAssessmentLines(assessment ParityAssessment, proof bool) []string {
	lines := []string{fmt.Sprintf("Operational parity: %d%%", assessment.Score)}
	limit := len(assessment.Deductions)
	if !proof && limit > 3 {
		limit = 3
	}
	for _, deduction := range assessment.Deductions[:limit] {
		lines = append(lines, fmt.Sprintf("Parity -%d: %s", deduction.Points, deduction.Summary))
	}
	if !proof && limit < len(assessment.Deductions) {
		lines = append(lines, fmt.Sprintf("%d additional parity deductions; use --proof for details.", len(assessment.Deductions)-limit))
	}
	if proof {
		for _, limitation := range assessment.Limitations {
			lines = append(lines, "Parity limitation: "+limitation)
		}
	}
	return lines
}

func sameJSON(a, b any) bool {
	left, _ := json.Marshal(a)
	right, _ := json.Marshal(b)
	return string(left) == string(right)
}
