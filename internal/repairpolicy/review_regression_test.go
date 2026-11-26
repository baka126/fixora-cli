package repairpolicy

import (
	"context"
	"testing"

	"github.com/fixora/kubectl-fixora/internal/kube"
	"gopkg.in/yaml.v3"
)

func TestPlacementRejectsMismatchedBaselineImage(t *testing.T) {
	s, a, patch := placementFixture(t)
	base := copyObject(a.object)
	spec, _ := kube.WorkloadPodSpec(base)
	spec["containers"].([]any)[0].(map[string]any)["image"] = "unrelated:old"
	raw, _ := yaml.Marshal(base)
	a, err := ReadBaseline(baselineFile(t, string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Candidate(s, a, "placement"); err == nil {
		t.Fatal("candidate accepted mismatched image")
	}

	_, err = Placement(context.Background(), s, a, patch, inspectLinux)
	if err == nil {
		t.Fatal("wrong explicitly supplied image accepted as placement authority")
	}
}
func TestCommonWitnessAcceptsCanonicalResourceQuantity(t *testing.T) {
	s := fixture(t)
	obj, _ := kube.WorkloadPatch("Deployment", "worker", "demo", map[string]any{"containers": []any{map[string]any{"name": "app", "resources": map[string]any{"requests": map[string]any{"cpu": "0.5"}}}}})
	raw, _ := yaml.Marshal(obj)
	patch := string(raw)
	proof, err := BindCommon(s, patch, "resources", func(string, string, string, string) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	pod := kube.Pod{Metadata: kube.ObjectMeta{Name: "shadow", Namespace: "demo", UID: "shadow-1", Labels: map[string]string{"fixora.io/sandbox": "true", "fixora.io/session": "session"}}, Spec: kube.PodSpec{Containers: []kube.Container{{Name: "app", Image: "image:v1", Resources: kube.ResourceRequirements{Requests: map[string]string{"cpu": "500m"}}}}}, Status: kube.PodStatus{Phase: "Running", Conditions: []kube.Condition{{Type: "Ready", Status: "True"}}, ContainerStatuses: []kube.ContainerStatus{{Name: "app", Ready: true}}}}
	if _, err = VerifyCommon(context.Background(), witnessReader{pod: pod}, proof, &pod, patch); err != nil {
		t.Fatalf("API-normalized equivalent quantity refused: %v", err)
	}
}
