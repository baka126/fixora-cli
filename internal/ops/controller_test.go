package ops

import (
	"context"
	"github.com/fixora/kubectl-fixora/internal/analyzer"
	"github.com/fixora/kubectl-fixora/internal/fix"
	"github.com/fixora/kubectl-fixora/internal/kube"
	"gopkg.in/yaml.v3"
	"testing"
	"time"
)

func TestControllerPreflightUpdateSemantics(t *testing.T) {
	for _, kind := range []string{"StatefulSet", "DaemonSet", "ReplicaSet", "Job", "CronJob", "Pod"} {
		t.Run(kind, func(t *testing.T) {
			spec := map[string]any{"containers": []any{map[string]any{"name": "app", "image": "busybox:1.36"}}}
			obj, _ := kube.WorkloadPatch(kind, "app", "ns", spec)
			obj["metadata"].(map[string]any)["uid"] = "u"
			f := analyzer.Finding{ResourceKind: kind, ResourceName: "app", Namespace: "ns"}
			p := fix.Plan{Resource: kind + "/app", Namespace: "ns", Strategy: "image", CanApply: true, ApplyEligible: true, Safe: true, Confidence: 95}
			r := controllerReader{object: obj}
			_, err := PreflightController(context.Background(), r, f, p)
			blocked := kind == "Job" || kind == "ReplicaSet"
			if (err != nil) != blocked {
				t.Fatalf("%s err=%v", kind, err)
			}
			if kind == "StatefulSet" || kind == "DaemonSet" {
				obj["spec"].(map[string]any)["updateStrategy"] = map[string]any{"type": "OnDelete"}
				if _, err := PreflightController(context.Background(), r, f, p); err == nil {
					t.Fatal("accepted OnDelete")
				}
			}
			if kind == "StatefulSet" {
				obj["spec"].(map[string]any)["updateStrategy"] = map[string]any{"type": "RollingUpdate", "rollingUpdate": map[string]any{"partition": float64(1)}}
				if _, err := PreflightController(context.Background(), r, f, p); err == nil {
					t.Fatal("accepted excluded partition")
				}
			}
		})
	}
}

type controllerReader struct{ object map[string]any }

func (r controllerReader) GetResource(context.Context, string, string) (map[string]any, error) {
	return r.object, nil
}
func (r controllerReader) GetPod(context.Context, string, string) (kube.Pod, error) {
	return kube.Pod{}, nil
}

func TestPreflightRejectsReplacedTarget(t *testing.T) {
	obj, _ := kube.WorkloadPatch("StatefulSet", "app", "ns", map[string]any{"containers": []any{map[string]any{"name": "app", "image": "busybox"}}})
	obj["metadata"].(map[string]any)["uid"] = "replacement"
	_, err := PreflightController(context.Background(), controllerReader{object: obj}, analyzer.Finding{ResourceKind: "StatefulSet", ResourceName: "app", Namespace: "ns"}, fix.Plan{Resource: "StatefulSet/app", Strategy: "image", CanApply: true, ApplyEligible: true, TargetUID: "original"})
	if err == nil {
		t.Fatal("accepted replacement")
	}
}
func TestContainerConfigIncludesProbesAndMounts(t *testing.T) {
	a := kube.Container{Name: "app", ReadinessProbe: map[string]any{"tcpSocket": map[string]any{"port": float64(8080)}}}
	b := a
	b.ReadinessProbe = map[string]any{"tcpSocket": map[string]any{"port": float64(9090)}}
	if sameContainerConfig([]kube.Container{a}, []kube.Container{b}) {
		t.Fatal("ignored stale probe")
	}
	b = a
	b.VolumeMounts = []map[string]any{{"name": "persistent", "mountPath": "/var/cache"}}
	if sameContainerConfig([]kube.Container{a}, []kube.Container{b}) {
		t.Fatal("ignored mounts")
	}
}
func TestPodVerificationRequiresPodReady(t *testing.T) {
	pod := kube.Pod{Metadata: kube.ObjectMeta{Name: "app", Namespace: "ns", UID: "u"}, Status: kube.PodStatus{Phase: "Running", ContainerStatuses: []kube.ContainerStatus{{Name: "app", Ready: true, Image: "busybox:1.36", ImageID: "sha256:abc", State: map[string]kube.StatusState{"running": {StartedAt: "now"}}}}, Conditions: []kube.Condition{{Type: "Ready", Status: "False"}}}}
	p := fix.Plan{TargetUID: "u", PatchTemplate: "apiVersion: v1\nkind: Pod\nspec:\n  containers:\n  - name: app\n    image: busybox:1.36\n"}
	got := VerifyPod(context.Background(), podReader{pod: pod}, analyzer.Finding{ResourceKind: "Pod", ResourceName: "app", Namespace: "ns"}, p, time.Millisecond)
	if got.Class == RolloutHealthy {
		t.Fatal("unavailable Pod reported healthy")
	}
}

type podReader struct {
	controllerReader
	pod kube.Pod
}

func (r podReader) GetPod(context.Context, string, string) (kube.Pod, error) { return r.pod, nil }

func TestRuntimeImageReferencesAreCanonical(t *testing.T) {
	for _, observed := range []string{"busybox:1.36", "docker.io/library/busybox:1.36", "registry-1.docker.io/library/busybox:1.36"} {
		if !runtimeImageMatches("busybox:1.36", kube.ContainerStatus{Image: observed, ImageID: "sha256:abc"}) {
			t.Fatalf("rejected %s", observed)
		}
	}
	if runtimeImageMatches("busybox:1.36", kube.ContainerStatus{Image: "busybox:1.35", ImageID: "sha256:abc"}) {
		t.Fatal("accepted old version")
	}
	if !runtimeImageMatches("busybox@sha256:abc", kube.ContainerStatus{Image: "busybox", ImageID: "docker-pullable://docker.io/library/busybox@sha256:abc"}) {
		t.Fatal("rejected resolved digest")
	}
}

func TestPodVerificationReadyWithCanonicalImage(t *testing.T) {
	pod := kube.Pod{Metadata: kube.ObjectMeta{Name: "app", Namespace: "ns", UID: "u"}, Spec: kube.PodSpec{Containers: []kube.Container{{Name: "app", Image: "busybox:1.36"}}}, Status: kube.PodStatus{Phase: "Running", ContainerStatuses: []kube.ContainerStatus{{Name: "app", Ready: true, Image: "docker.io/library/busybox:1.36", ImageID: "sha256:abc", State: map[string]kube.StatusState{"running": {StartedAt: "now"}}}}, Conditions: []kube.Condition{{Type: "Ready", Status: "True"}}}}
	p := fix.Plan{TargetUID: "u", PatchTemplate: "apiVersion: v1\nkind: Pod\nspec:\n  containers:\n  - name: app\n    image: busybox:1.36\n"}
	got := VerifyPod(context.Background(), podReader{pod: pod}, analyzer.Finding{ResourceKind: "Pod", ResourceName: "app", Namespace: "ns"}, p, time.Millisecond)
	if got.Class != RolloutHealthy {
		t.Fatalf("%+v", got)
	}
	pod.Metadata.UID = "replacement"
	got = VerifyPod(context.Background(), podReader{pod: pod}, analyzer.Finding{ResourceKind: "Pod", ResourceName: "app", Namespace: "ns"}, p, time.Millisecond)
	if got.Class != RolloutInvalid {
		t.Fatal("replacement reported healthy")
	}
}
func TestControllerReadbackRequiresAppliedTemplateAndFleetReadiness(t *testing.T) {
	for _, kind := range []string{"StatefulSet", "DaemonSet"} {
		t.Run(kind, func(t *testing.T) {
			obj, _ := kube.WorkloadPatch(kind, "app", "ns", map[string]any{"containers": []any{map[string]any{"name": "app", "image": "busybox:1.36"}}})
			data, _ := yaml.Marshal(obj)
			obj["metadata"].(map[string]any)["uid"] = "u"
			obj["metadata"].(map[string]any)["generation"] = float64(2)
			obj["status"] = map[string]any{"observedGeneration": float64(2), "readyReplicas": float64(2), "updatedReplicas": float64(2), "currentRevision": "r", "updateRevision": "r", "desiredNumberScheduled": float64(2), "updatedNumberScheduled": float64(2), "numberReady": float64(2), "numberAvailable": float64(2)}
			if kind == "StatefulSet" {
				obj["spec"].(map[string]any)["replicas"] = float64(2)
			}
			r := controllerRolloutReader{fakeRolloutChecker: fakeRolloutChecker{ok: true}, controllerReader: controllerReader{object: obj}}
			f := analyzer.Finding{ResourceKind: kind, ResourceName: "app", Namespace: "ns"}
			p := fix.Plan{TargetUID: "u", PatchTemplate: string(data)}
			if out := VerifyRollout(context.Background(), r, f, p, time.Second); out.Class != RolloutHealthy {
				t.Fatal(out)
			}
			spec, _ := kube.WorkloadPodSpec(obj)
			spec["containers"].([]any)[0].(map[string]any)["image"] = "busybox:1.35"
			if out := VerifyRollout(context.Background(), r, f, p, time.Second); out.Class == RolloutHealthy {
				t.Fatal("old template reported healthy")
			}
		})
	}
}

type controllerRolloutReader struct {
	fakeRolloutChecker
	controllerReader
}

func TestCronJobAppliedTemplateRemainsPending(t *testing.T) {
	obj, _ := kube.WorkloadPatch("CronJob", "nightly", "prod", map[string]any{"containers": []any{map[string]any{"name": "app", "image": "busybox:1.36"}}})
	data, _ := yaml.Marshal(obj)
	obj["metadata"].(map[string]any)["uid"] = "u"
	r := controllerCompletionReader{fakeCompletionChecker: fakeCompletionChecker{cron: kube.CronJobState{RecentJobFailed: true}}, controllerReader: controllerReader{object: obj}}
	out := VerifyCompletion(context.Background(), r, cronFinding(), fix.Plan{TargetUID: "u", PatchTemplate: string(data)}, time.Second)
	if out.Class != CronJobPending || out.Rollback.Command != "" {
		t.Fatalf("past failed run must not establish post-fix failure: %+v", out)
	}
}

type controllerCompletionReader struct {
	fakeCompletionChecker
	controllerReader
}

func TestResourceReadbackUsesQuantitySemantics(t *testing.T) {
	live := map[string]any{"containers": []any{map[string]any{"name": "app", "resources": map[string]any{"limits": map[string]any{"memory": "512Mi", "cpu": "100m"}}}}}
	proposed := map[string]any{"containers": []any{map[string]any{"name": "app", "resources": map[string]any{"limits": map[string]any{"memory": "0.5Gi", "cpu": "0.1"}}}}}
	if !patchFieldsMatch(live, proposed) {
		t.Fatal("API canonicalization treated as different resources")
	}
	proposed["containers"].([]any)[0].(map[string]any)["resources"].(map[string]any)["limits"].(map[string]any)["memory"] = "1Gi"
	if patchFieldsMatch(live, proposed) {
		t.Fatal("different quantity matched")
	}
}
