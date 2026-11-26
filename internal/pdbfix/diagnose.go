package pdbfix

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func ready(p corev1.Pod) bool {
	if p.DeletionTimestamp != nil || p.Status.Phase != corev1.PodRunning {
		return false
	}
	for _, c := range p.Status.Conditions {
		if c.Type == corev1.PodReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}
func Diagnose(s Snapshot) []Diagnosis {
	var ds []Diagnosis
	add := func(c, sev, cause, next string) {
		for _, d := range ds {
			if d.Code == c {
				return
			}
		}
		ds = append(ds, Diagnosis{c, sev, cause, next})
	}
	if len(s.Issues) > 0 {
		add("PDBEvidenceUnavailable", "medium", "Some disruption-policy evidence could not be read or validated.", "Restore access to complete Pod, PDB and autoscaler inventories; inspect proof for the failed reads.")
	}
	if s.PDB == nil {
		return ds
	}
	p := s.PDB
	count, unhealthy := 0, 0
	owners := map[string]bool{}
	unsupported := false
	overlap := false
	for _, pod := range s.Pods {
		if !selected(p.Spec.Selector, pod.Labels) {
			continue
		}
		count++
		if !ready(pod) {
			unhealthy++
		}
		o, ok := s.Owners[string(pod.UID)]
		if !ok || o.UID == "" || (o.Kind != "Deployment" && o.Kind != "StatefulSet") {
			unsupported = true
		} else {
			owners[o.UID] = true
		}
		for _, b := range s.Budgets {
			if b.UID != p.UID && selected(b.Spec.Selector, pod.Labels) {
				overlap = true
			}
		}
	}
	if count == 0 && len(s.Issues) == 0 {
		add("PDBNoSelectedPods", "medium", "The PDB selector currently matches no Pods; the intended workload is not established.", "Confirm the intended workload and selector before proposing a correction.")
	}
	if unsupported || len(owners) > 1 {
		add("PDBUnsupportedOwnership", "medium", "Selected Pods have mixed, unsupported or unverified ownership.", "Review Pod owner identities and the intended protection boundary.")
	}
	if overlap {
		add("PDBOverlap", "high", "Multiple PDBs select at least one of the same Pods.", "Review overlapping protection policies; Fixora will not delete or relax another budget.")
	}
	fresh := p.Status.ObservedGeneration == p.Generation
	if !fresh {
		add("PDBStatusStale", "medium", "PDB status has not observed the current policy generation.", "Wait for disruption-controller reconciliation and inspect its events.")
	}
	failed := false
	for _, c := range p.Status.Conditions {
		if c.Reason == "SyncFailed" && c.Status == metav1.ConditionFalse {
			failed = true
			add("PDBReconciliationFailed", "high", "The disruption controller could not reconcile the PDB.", "Inspect the PDB condition and controller/scale-target errors.")
		}
	}
	if count > 0 && (unhealthy > 0 || (fresh && !failed && p.Status.CurrentHealthy < p.Status.DesiredHealthy)) {
		add("DisruptionsBlocked", "high", "Selected replicas are unavailable or do not meet the budget's healthy replica requirement.", "Repair unavailable replicas before considering a change to disruption protection.")
	}
	if count > 0 && unhealthy == 0 && fresh && !failed && p.Status.CurrentHealthy >= p.Status.DesiredHealthy && p.Status.DisruptionsAllowed == 0 && len(s.Issues) == 0 && !overlap && !unsupported && len(owners) == 1 {
		add("DisruptionProtectionActive", "info", "The workload is healthy and its current budget permits no additional voluntary disruption.", "Keep the protection unless an independently approved availability policy calls for a change.")
	}
	return ds
}
