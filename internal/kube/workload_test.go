package kube

import "testing"

func TestWorkloadSpecRoundTrip(t *testing.T) {
	for _, kind := range []string{"Pod", "Deployment", "StatefulSet", "DaemonSet", "ReplicaSet", "Job", "CronJob"} {
		t.Run(kind, func(t *testing.T) {
			spec := map[string]any{"containers": []any{map[string]any{"name": "app", "image": "busybox:1.36"}}}
			obj, err := WorkloadPatch(kind, "demo", "ns", spec)
			if err != nil {
				t.Fatal(err)
			}
			got, ok := WorkloadPodSpec(obj)
			if !ok || got["containers"] == nil {
				t.Fatalf("lost spec: %v", obj)
			}
			if err := ValidateWorkloadPatchEnvelope(obj, kind); err != nil {
				t.Fatal(err)
			}
			obj["spec"].(map[string]any)["replicas"] = 3
			if kind != "Pod" && ValidateWorkloadPatchEnvelope(obj, kind) == nil {
				t.Fatal("accepted replicas mutation")
			}
		})
	}
}
func TestWorkloadEnvelopeRejectsAmbiguousAndWrongVersion(t *testing.T) {
	obj, _ := WorkloadPatch("CronJob", "demo", "ns", map[string]any{"containers": []any{}})
	obj["spec"].(map[string]any)["template"] = map[string]any{"spec": map[string]any{"containers": []any{}}}
	if _, ok := WorkloadPodSpec(obj); ok {
		t.Fatal("accepted ambiguous envelope")
	}
	delete(obj["spec"].(map[string]any), "template")
	obj["apiVersion"] = "apps/v1"
	if ValidateWorkloadPatchEnvelope(obj, "CronJob") == nil {
		t.Fatal("accepted wrong version")
	}
}
