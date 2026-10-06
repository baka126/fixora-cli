package repairpolicy

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/fixora/kubectl-fixora/internal/kube"
	"gopkg.in/yaml.v3"
)

func startupSnapshot(t *testing.T, kind string) Snapshot {
	s := fixture(t)
	s.Target.Kind = kind
	s.Target.PodName = "failed"
	s.Target.PodUID = "pod-1"
	spec, _ := kube.WorkloadPodSpec(s.Object)
	obj, err := kube.WorkloadPatch(kind, s.Target.Name, s.Target.Namespace, spec)
	if err != nil {
		t.Fatal(err)
	}
	obj["metadata"].(map[string]any)["uid"] = s.Target.UID
	s.Object = obj
	s.Pod = &kube.Pod{Metadata: kube.ObjectMeta{Name: "failed", Namespace: "demo", UID: "pod-1"}, Spec: kube.PodSpec{Containers: []kube.Container{{Name: "app", Image: "image:v1", Command: []string{"old"}}}}, Status: kube.PodStatus{ContainerStatuses: []kube.ContainerStatus{{Name: "app", State: map[string]kube.StatusState{"waiting": {Reason: "CrashLoopBackOff"}}, LastState: map[string]kube.StatusState{"terminated": {ExitCode: 1}}}}}}
	if kind == "Pod" {
		s.Target.Name = "failed"
		s.Target.UID = "pod-1"
		meta := s.Object["metadata"].(map[string]any)
		meta["name"] = "failed"
		meta["uid"] = "pod-1"
	}
	return s
}
func startupAuthority(t *testing.T, s Snapshot) *Authority {
	obj := copyObject(s.Object)
	spec, _ := kube.WorkloadPodSpec(obj)
	cs := spec["containers"].([]any)
	cs[0].(map[string]any)["command"] = []any{"approved"}
	cs[0].(map[string]any)["args"] = []any{}
	data, _ := yaml.Marshal(obj)
	a, err := ReadBaseline(baselineFile(t, string(data)))
	if err != nil {
		t.Fatal(err)
	}
	return a
}
func TestStartupSevenEnvelopesAndUniqueRegularContainer(t *testing.T) {
	for _, kind := range []string{"Deployment", "StatefulSet", "DaemonSet", "ReplicaSet", "Job", "CronJob", "Pod"} {
		t.Run(kind, func(t *testing.T) {
			s := startupSnapshot(t, kind)
			spec, _ := kube.WorkloadPodSpec(s.Object)
			spec["containers"] = append(spec["containers"].([]any), map[string]any{"name": "sidecar", "image": "side:v1"})
			s.Pod.Spec.Containers = append(s.Pod.Spec.Containers, kube.Container{Name: "sidecar", Image: "side:v1"})
			s.Pod.Status.ContainerStatuses = append(s.Pod.Status.ContainerStatuses, kube.ContainerStatus{Name: "sidecar", Ready: true})
			patch, p, err := Startup(s, startupAuthority(t, s), "")
			if err != nil {
				t.Fatal(err)
			}
			if err = p.Check(s, patch); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(patch, "sidecar") || strings.Contains(patch, "image:") {
				t.Fatal("changed unrelated fields")
			}
			var obj map[string]any
			_ = yaml.Unmarshal([]byte(patch), &obj)
			if err = kube.ValidateWorkloadPatchEnvelope(obj, kind); err != nil {
				t.Fatal(err)
			}
		})
	}
}
func TestStartupRefusesWrongImageStalePodAndAmbiguousFailure(t *testing.T) {
	for _, bad := range []string{"image", "stale", "multiple", "init", "recovered", "command", "uid"} {
		t.Run(bad, func(t *testing.T) {
			s := startupSnapshot(t, "Deployment")
			a := startupAuthority(t, s)
			switch bad {
			case "image":
				a.object["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)["containers"].([]any)[0].(map[string]any)["image"] = "other"
				a.digest = digest(a.object)
			case "stale":
				s.Pod.Spec.Containers[0].Command = []string{"another"}
			case "multiple":
				s.Pod.Status.ContainerStatuses = append(s.Pod.Status.ContainerStatuses, s.Pod.Status.ContainerStatuses[0])
			case "init":
				s.Pod.Status.InitStatuses = []kube.ContainerStatus{{Name: "init", State: map[string]kube.StatusState{"terminated": {ExitCode: 1}}}}
			case "recovered":
				s.Pod.Status.ContainerStatuses[0].Ready = true
			case "command":
				a.object["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)["containers"].([]any)[0].(map[string]any)["command"] = nil
				a.digest = digest(a.object)
			case "uid":
				s.Pod.Metadata.UID = "replacement"
			}
			if _, _, err := Startup(s, a, ""); err == nil {
				t.Fatal("unproven startup accepted")
			}
		})
	}
}
func TestStartupArgsRestorationIsExact(t *testing.T) {
	s := startupSnapshot(t, "Deployment")
	spec, _ := kube.WorkloadPodSpec(s.Object)
	spec["containers"].([]any)[0].(map[string]any)["args"] = []any{"obsolete"}
	s.Pod.Spec.Containers[0].Args = []string{"obsolete"}
	a := startupAuthority(t, s)
	patch, _, err := Startup(s, a, "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(patch, "args: []") {
		t.Fatal(patch)
	}
}

type witnessReader struct {
	kube.Reader
	pod kube.Pod
}

func (r witnessReader) GetPod(context.Context, string, string) (kube.Pod, error) { return r.pod, nil }
func TestStartupReceiptPinsPatchAuthorityAndUID(t *testing.T) {
	s := startupSnapshot(t, "Deployment")
	patch, p, err := Startup(s, startupAuthority(t, s), "")
	if err != nil {
		t.Fatal(err)
	}
	pod := *s.Pod
	pod.Metadata = kube.ObjectMeta{Name: "shadow", Namespace: "demo", UID: "shadow-1", Labels: map[string]string{"fixora.io/session": "session-1", "fixora.io/sandbox": "true"}}
	pod.Spec.Containers[0].Command = []string{"approved"}
	pod.Status.Phase = "Running"
	pod.Status.Conditions = []kube.Condition{{Type: "Ready", Status: "True"}}
	pod.Status.ContainerStatuses[0].Ready = true
	r, err := VerifyStartup(context.Background(), witnessReader{pod: pod}, p, &pod, patch)
	if err != nil {
		t.Fatal(err)
	}
	if err = r.Check(p, patch); err != nil {
		t.Fatal(err)
	}
	if err = r.Check(p, patch+"\n"); err == nil {
		t.Fatal("edited patch reused receipt")
	}
	raw, _ := json.Marshal(r)
	var restored Receipt
	_ = json.Unmarshal(raw, &restored)
	if err = restored.Check(p, patch); err == nil {
		t.Fatal("saved receipt trusted")
	}
	pod.Spec.Containers[0].Command = []string{"guessed"}
	if _, err = VerifyStartup(context.Background(), witnessReader{pod: pod}, p, &pod, patch); err == nil {
		t.Fatal("wrong command verified")
	}
}
