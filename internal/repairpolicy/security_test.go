package repairpolicy

import (
	"context"
	"testing"

	"github.com/fixora/kubectl-fixora/internal/kube"
	"gopkg.in/yaml.v3"
)

func securityFixture(t *testing.T) (Snapshot, *Authority, string) {
	s := fixture(t)
	spec, _ := kube.WorkloadPodSpec(s.Object)
	spec["containers"].([]any)[0].(map[string]any)["securityContext"] = map[string]any{"runAsUser": float64(1001)}
	s.Namespace = map[string]any{"kind": "Namespace", "metadata": map[string]any{"name": "demo", "uid": "ns-1", "labels": map[string]any{"pod-security.kubernetes.io/enforce": "restricted", "pod-security.kubernetes.io/enforce-version": "v1.33"}}}
	revision := copyObject(s.Object)
	revision["kind"] = "ReplicaSet"
	meta := revision["metadata"].(map[string]any)
	meta["name"] = "worker-new"
	meta["uid"] = "rs-1"
	meta["generation"] = float64(2)
	meta["ownerReferences"] = []any{map[string]any{"kind": "Deployment", "apiVersion": "apps/v1", "name": "worker", "uid": "owner-1", "controller": true}}
	revision["spec"].(map[string]any)["replicas"] = float64(1)
	revision["status"] = map[string]any{"observedGeneration": float64(2), "conditions": []any{map[string]any{"type": "ReplicaFailure", "status": "True", "reason": "FailedCreate", "message": "violates PodSecurity: runAsNonRoot != true, allowPrivilegeEscalation != false, unrestricted capabilities, seccompProfile"}}}
	s.Revision = revision
	s.Events = []kube.Event{{InvolvedObject: kube.ObjectReference{Kind: "ReplicaSet", Name: "worker-new", Namespace: "demo", UID: "rs-1"}, Reason: "FailedCreate", Message: "violates PodSecurity: runAsNonRoot != true, allowPrivilegeEscalation != false, unrestricted capabilities, seccompProfile"}}
	obj := copyObject(s.Object)
	approved, _ := kube.WorkloadPodSpec(obj)
	sc := approved["containers"].([]any)[0].(map[string]any)["securityContext"].(map[string]any)
	sc["runAsNonRoot"] = true
	sc["allowPrivilegeEscalation"] = false
	sc["seccompProfile"] = map[string]any{"type": "RuntimeDefault"}
	sc["capabilities"] = map[string]any{"drop": []any{"ALL"}}
	raw, _ := yaml.Marshal(obj)
	a, err := ReadBaseline(baselineFile(t, string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	patch, err := Candidate(s, a, "security")
	if err != nil {
		t.Fatal(err)
	}
	return s, a, patch
}
func TestSecurityEffectivePositiveUIDAndExactApprovedStrengthening(t *testing.T) {
	s, a, patch := securityFixture(t)
	p, err := Security(s, a, patch)
	if err != nil {
		t.Fatal(err)
	}
	if err = p.Check(s, patch); err != nil {
		t.Fatal(err)
	}
}
func TestSecurityRefusesRelaxationIdentityGuessAndUnprovedPolicy(t *testing.T) {
	for _, bad := range []string{"uid", "policy", "historical", "field", "privileged", "recovered"} {
		t.Run(bad, func(t *testing.T) {
			s, a, patch := securityFixture(t)
			spec, _ := kube.WorkloadPodSpec(s.Object)
			sc := spec["containers"].([]any)[0].(map[string]any)["securityContext"].(map[string]any)
			switch bad {
			case "uid":
				sc["runAsUser"] = float64(0)
			case "policy":
				s.Namespace["metadata"].(map[string]any)["labels"].(map[string]any)["pod-security.kubernetes.io/enforce"] = "baseline"
			case "historical":
				s.Events[0].InvolvedObject.UID = "old"
			case "field":
				s.Events[0].Message = "violates PodSecurity: unrelated"
				s.Revision["status"].(map[string]any)["conditions"].([]any)[0].(map[string]any)["message"] = "violates PodSecurity: unrelated"
			case "privileged":
				sc["privileged"] = true
			case "recovered":
				s.Revision["status"].(map[string]any)["readyReplicas"] = float64(1)
			}
			if _, err := Security(s, a, patch); err == nil {
				t.Fatal("unproved or unsafe strengthening accepted")
			}
		})
	}
}
func TestSecurityAdmissionAcceptsButRuntimeFailureRejectsReceipt(t *testing.T) {
	s, a, patch := securityFixture(t)
	p, err := Security(s, a, patch)
	if err != nil {
		t.Fatal(err)
	}
	pod := kube.Pod{Metadata: kube.ObjectMeta{Name: "shadow", Namespace: "demo", UID: "shadow-1", Labels: map[string]string{"fixora.io/sandbox": "true", "fixora.io/session": "session"}}, Spec: kube.PodSpec{Containers: []kube.Container{{Name: "app", Image: "image:v1", SecurityContext: map[string]any{"runAsUser": float64(1001), "runAsNonRoot": true, "allowPrivilegeEscalation": false, "seccompProfile": map[string]any{"type": "RuntimeDefault"}, "capabilities": map[string]any{"drop": []any{"ALL"}}}}}}, Status: kube.PodStatus{Phase: "Running", Conditions: []kube.Condition{{Type: "Ready", Status: "True"}}, ContainerStatuses: []kube.ContainerStatus{{Name: "app", Ready: true}}}}
	reader := securityReader{witnessReader: witnessReader{pod: pod}, namespace: s.Namespace}
	if _, err = VerifySecurity(context.Background(), reader, p, &pod, patch); err != nil {
		t.Fatal(err)
	}
	pod.Status.ContainerStatuses[0].Ready = false
	reader.pod = pod
	if _, err = VerifySecurity(context.Background(), reader, p, &pod, patch); err == nil {
		t.Fatal("admission-only success accepted")
	}
	pod.Status.ContainerStatuses[0].Ready = true
	pod.Spec.Containers[0].SecurityContext["runAsUser"] = float64(2000)
	reader.pod = pod
	if _, err = VerifySecurity(context.Background(), reader, p, &pod, patch); err == nil {
		t.Fatal("changed application identity accepted")
	}
}

type securityReader struct {
	witnessReader
	namespace map[string]any
}

func (r securityReader) GetResource(context.Context, string, string) (map[string]any, error) {
	return r.namespace, nil
}
