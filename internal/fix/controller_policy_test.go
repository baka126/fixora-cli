package fix

import (
	"github.com/fixora/kubectl-fixora/internal/analyzer"
	"github.com/fixora/kubectl-fixora/internal/kube"
	"gopkg.in/yaml.v3"
	"testing"
)

func TestControllerFamilyDeliveryPolicy(t *testing.T) {
	for _, kind := range []string{"Deployment", "StatefulSet", "DaemonSet", "ReplicaSet", "Job", "CronJob", "Pod"} {
		for _, strategy := range []string{"image", "resources", "env", "probe", "filesystem"} {
			t.Run(kind+"/"+strategy, func(t *testing.T) {
				obj, _ := kube.WorkloadPatch(kind, "app", "ns", map[string]any{"containers": []any{map[string]any{"name": "app", "image": "busybox:1.36"}}})
				data, _ := yaml.Marshal(obj)
				p := WithTargetPolicy(Plan{Resource: kind + "/app", Namespace: "ns", Strategy: strategy, PatchTemplate: string(data), CanApply: true, ApplyEligible: true, Confidence: 95, Safe: true})
				want := kind != "ReplicaSet" && kind != "Job" && (kind != "Pod" || strategy == "image") && !(kind == "DaemonSet" && strategy == "resources")
				if p.ApplyEligible != want {
					t.Fatalf("eligible=%v want=%v: %+v", p.ApplyEligible, want, p)
				}
				if p.PatchTemplate == "" {
					t.Fatal("lost review proposal")
				}
				p.refreshApplyEligibility(true)
				if !want && p.ApplyEligible {
					t.Fatal("force bypassed policy")
				}
			})
		}
	}
}
func TestFilesystemAndConnectionWrappersAllKinds(t *testing.T) {
	for _, kind := range []string{"Deployment", "StatefulSet", "DaemonSet", "ReplicaSet", "Job", "CronJob", "Pod"} {
		f := analyzer.Finding{ResourceKind: kind, ResourceName: "app", Namespace: "ns", Status: "PermissionDenied"}
		p := WithEphemeralCachePatch(BuildPlan(f), EphemeralCache{Container: "app", Directory: "/var/cache", Group: 1000})
		var obj map[string]any
		if err := yaml.Unmarshal([]byte(p.PatchTemplate), &obj); err != nil {
			t.Fatal(err)
		}
		if err := kube.ValidateWorkloadPatchEnvelope(obj, kind); err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
		p = WithConnectionConfigPatch(BuildPlan(f), ConnectionConfig{Container: "app", ConfigMap: "connection", Keys: []string{"DB_HOST"}})
		if err := yaml.Unmarshal([]byte(p.PatchTemplate), &obj); err != nil {
			t.Fatal(err)
		}
		if err := kube.ValidateWorkloadPatchEnvelope(obj, kind); err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
	}
}
