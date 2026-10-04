package infer

import (
	"context"
	"github.com/fixora/kubectl-fixora/internal/analyzer"
	"github.com/fixora/kubectl-fixora/internal/fix"
	"testing"
)

type filesystemReader struct {
	kubeReader
	pod map[string]any
}

func (r filesystemReader) GetResource(_ context.Context, _, _ string) (map[string]any, error) {
	return r.pod, nil
}
func filesystemPod(command string) map[string]any {
	return map[string]any{"metadata": map[string]any{"name": "worker-pod", "namespace": "demo"}, "status": map[string]any{"containerStatuses": []any{map[string]any{"name": "worker", "ready": false, "state": map[string]any{"waiting": map[string]any{"reason": "CrashLoopBackOff"}}, "lastState": map[string]any{"terminated": map[string]any{"reason": "Error", "exitCode": float64(1)}}}}}, "spec": map[string]any{"containers": []any{map[string]any{"name": "worker", "command": []any{"sh", "-c", command}, "securityContext": map[string]any{"runAsNonRoot": true, "readOnlyRootFilesystem": true, "runAsUser": float64(1000)}}}}}
}
func TestFilesystemInferrerProvesCacheMount(t *testing.T) {
	f := analyzer.Finding{Namespace: "demo", ResourceKind: "Deployment", ResourceName: "worker", PodName: "worker-pod", Status: "PermissionDenied", Logs: []analyzer.LogSnippet{{Text: "permission denied: cannot write cache"}}}
	r := filesystemReader{pod: filesystemPod("if ! touch /var/cache/app.tmp 2>/dev/null; then echo 'permission denied: cannot write cache'; exit 1; fi; sleep 3600")}
	p, ok := Concrete(context.Background(), r, f, fix.BuildPlan(f))
	if !ok || !p.ApplyEligible || p.Strategy != "filesystem" {
		t.Fatalf("expected cache remediation: %+v", p)
	}
}
func TestFilesystemInferrerDeclinesWithoutProof(t *testing.T) {
	for _, command := range []string{"touch $CACHE_FILE", "touch /etc/settings", "touch /var/cache/a; touch /var/cache/b", "echo touch /var/cache/a"} {
		f := analyzer.Finding{Namespace: "demo", ResourceKind: "Deployment", ResourceName: "worker", PodName: "worker-pod", Status: "PermissionDenied", Logs: []analyzer.LogSnippet{{Text: "permission denied: cannot write cache"}}}
		if _, ok, _ := (filesystemInferrer{}).Infer(context.Background(), filesystemReader{pod: filesystemPod(command)}, f, fix.BuildPlan(f)); ok {
			t.Fatalf("accepted unproven command %q", command)
		}
	}
}

func TestFilesystemInferrerDeclinesUnsafePodContext(t *testing.T) {
	for _, scenario := range []string{"writable-root", "root-user", "mounted-data", "persistent-volume", "missing-log", "init-container", "invalid-group", "ready", "wrong-pod", "oom"} {
		pod := filesystemPod("if ! touch /var/cache/app.tmp; then echo 'permission denied: cannot write cache'; exit 1; fi")
		spec := pod["spec"].(map[string]any)
		container := spec["containers"].([]any)[0].(map[string]any)
		sc := container["securityContext"].(map[string]any)
		f := analyzer.Finding{Namespace: "demo", ResourceKind: "Deployment", ResourceName: "worker", PodName: "worker-pod", Status: "PermissionDenied", Logs: []analyzer.LogSnippet{{Text: "permission denied: cannot write cache"}}}
		switch scenario {
		case "ready":
			pod["status"].(map[string]any)["containerStatuses"].([]any)[0].(map[string]any)["ready"] = true
		case "wrong-pod":
			pod["metadata"].(map[string]any)["name"] = "different"
		case "oom":
			pod["status"].(map[string]any)["containerStatuses"].([]any)[0].(map[string]any)["lastState"] = map[string]any{"terminated": map[string]any{"reason": "OOMKilled", "exitCode": float64(137)}}
		case "writable-root":
			sc["readOnlyRootFilesystem"] = false
		case "root-user":
			sc["runAsUser"] = float64(0)
		case "mounted-data":
			container["volumeMounts"] = []any{map[string]any{"name": "data", "mountPath": "/var/cache"}}
		case "persistent-volume":
			spec["volumes"] = []any{map[string]any{"name": "data", "persistentVolumeClaim": map[string]any{"claimName": "cache"}}}
		case "missing-log":
			f.Logs = nil
		case "init-container":
			spec["initContainers"] = []any{map[string]any{"name": "setup"}}
		case "invalid-group":
			spec["securityContext"] = map[string]any{"fsGroup": float64(0)}
		}
		if _, ok, _ := (filesystemInferrer{}).Infer(context.Background(), filesystemReader{pod: pod}, f, fix.BuildPlan(f)); ok {
			t.Fatalf("accepted %s", scenario)
		}
	}
}
