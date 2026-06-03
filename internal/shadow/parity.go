package shadow

import (
	"encoding/json"

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

func sameJSON(a, b any) bool {
	left, _ := json.Marshal(a)
	right, _ := json.Marshal(b)
	return string(left) == string(right)
}
