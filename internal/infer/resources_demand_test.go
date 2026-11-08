package infer

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/fixora/kubectl-fixora/internal/analyzer"
	"github.com/fixora/kubectl-fixora/internal/fix"
)

type demandReader struct {
	kubeReader
	pod map[string]any
}

func (r demandReader) GetResource(_ context.Context, namespace, resource string) (map[string]any, error) {
	if namespace != "default" || resource != "pod/oom-pod" {
		panic("unexpected pod read")
	}
	return r.pod, nil
}
func demandPod(t *testing.T, spec string) map[string]any {
	t.Helper()
	var p map[string]any
	if err := json.Unmarshal([]byte(`{"metadata":{"name":"oom-pod","namespace":"default"},"spec":`+spec+`,"status":{"containerStatuses":[{"name":"app","lastState":{"terminated":{"reason":"OOMKilled"}}}]}}`), &p); err != nil {
		t.Fatal(err)
	}
	return p
}
func demandFinding() analyzer.Finding {
	return analyzer.Finding{Namespace: "default", PodName: "oom-pod", ResourceKind: "Deployment", ResourceName: "oom", Status: "OOMKilled", Evidence: []analyzer.Evidence{{Label: "Container image app", Value: "polinux/stress"}}}
}
func TestResourcesDeclaredStressDemandWithoutMetrics(t *testing.T) {
	f := demandFinding()
	r := demandReader{pod: demandPod(t, `{"containers":[{"name":"app","image":"polinux/stress","args":["--vm","1","--vm-bytes","100M","--vm-hang","1"],"resources":{"limits":{"memory":"20Mi"}}}]}`)}
	p, ok := Concrete(context.Background(), r, f, fix.BuildPlan(f))
	if !ok || !p.ApplyEligible {
		t.Fatalf("declared stress demand must yield eligible plan: %+v", p)
	}
	result, ok, _ := resourcesInferrer{}.Infer(context.Background(), r, f, fix.Plan{Strategy: "resources"})
	if !ok || result.Options.MemoryRequest != "112Mi" || result.Options.MemoryLimit != "160Mi" {
		t.Fatalf("want demand100Mi request112Mi limit160Mi, got %+v", result)
	}
}
func TestResourcesDeclaredStressDemandOverridesClippedMetric(t *testing.T) {
	f := demandFinding()
	f.Evidence = append(f.Evidence, analyzer.Evidence{Label: "Metrics pod containers", Value: "p app 3m 18Mi"})
	r := demandReader{pod: demandPod(t, `{"containers":[{"name":"app","image":"polinux/stress","args":["--vm","2","--vm-bytes","100M"],"resources":{"limits":{"memory":"20Mi"}}}]}`)}
	result, ok, _ := resourcesInferrer{}.Infer(context.Background(), r, f, fix.Plan{Strategy: "resources"})
	if !ok || result.Options.MemoryRequest != "208Mi" || result.Options.MemoryLimit != "304Mi" {
		t.Fatalf("want two workers200Mi request208Mi limit304Mi, got %+v", result)
	}
}
func TestResourcesDeclaredDemandDeclinesAmbiguousOrUnrelatedWorkload(t *testing.T) {
	for _, tc := range []struct{ name, image, args, status string }{
		{"unknown image", "example/stress-wrapper", `["--vm","1","--vm-bytes","100M"]`, "OOMKilled"},
		{"unknown option", "polinux/stress", `["--vm","1","--vm-bytes","100M","--dry-run"]`, "OOMKilled"},
		{"percentage", "polinux/stress", `["--vm","1","--vm-bytes","100%"]`, "OOMKilled"},
		{"duplicate worker count", "polinux/stress", `["--vm","1","--vm","2","--vm-bytes","100M"]`, "OOMKilled"},
		{"overflow", "polinux/stress", `["--vm","999999999999999999","--vm-bytes","100G"]`, "OOMKilled"},
		{"not OOM", "polinux/stress", `["--vm","1","--vm-bytes","100M"]`, "Error"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := demandFinding()
			f.Evidence[0].Value = tc.image
			pod := demandPod(t, `{"containers":[{"name":"app","image":"`+tc.image+`","args":`+tc.args+`,"resources":{"limits":{"memory":"20Mi"}}}]}`)
			pod["status"].(map[string]any)["containerStatuses"].([]any)[0].(map[string]any)["lastState"].(map[string]any)["terminated"].(map[string]any)["reason"] = tc.status
			if _, ok, _ := (resourcesInferrer{}).Infer(context.Background(), demandReader{pod: pod}, f, fix.Plan{Strategy: "resources"}); ok {
				t.Fatal("ambiguous or unrelated allocation must decline")
			}
		})
	}
}

func TestResourcesDeclaredDDDemandRequiresMemoryVolume(t *testing.T) {
	for _, tc := range []struct {
		medium string
		want   bool
	}{{"Memory", true}, {"", false}} {
		t.Run(tc.medium, func(t *testing.T) {
			f := demandFinding()
			f.Evidence[0].Value = "busybox:1.36"
			pod := demandPod(t, `{"containers":[{"name":"app","image":"busybox:1.36","command":["sh","-c","dd if=/dev/zero of=/cache/fill bs=1M count=64; sleep 3600"],"resources":{"limits":{"memory":"32Mi"}},"volumeMounts":[{"name":"cache","mountPath":"/cache"}]}],"volumes":[{"name":"cache","emptyDir":{"medium":"`+tc.medium+`"}}]}`)
			result, ok, _ := resourcesInferrer{}.Infer(context.Background(), demandReader{pod: pod}, f, fix.Plan{Strategy: "resources"})
			if ok != tc.want {
				t.Fatalf("accept=%v want=%v", ok, tc.want)
			}
			if ok && (result.Options.MemoryRequest != "64Mi" || result.Options.MemoryLimit != "96Mi") {
				t.Fatalf("want64/96Mi, got%+v", result)
			}
		})
	}
}

func TestResourcesDeclaredDDDemandAcceptsContainerRuntimeOOMMessage(t *testing.T) {
	f := demandFinding()
	f.Evidence[0].Value = "busybox:1.36"
	pod := demandPod(t, `{"containers":[{"name":"app","image":"busybox:1.36","command":["sh","-c","dd if=/dev/zero of=/cache/fill bs=1M count=64; sleep 3600"],"resources":{"limits":{"memory":"32Mi"}},"volumeMounts":[{"name":"cache","mountPath":"/cache"}]}],"volumes":[{"name":"cache","emptyDir":{"medium":"Memory"}}]}`)
	status := pod["status"].(map[string]any)["containerStatuses"].([]any)[0].(map[string]any)
	status["lastState"].(map[string]any)["terminated"].(map[string]any)["reason"] = "StartError"
	status["lastState"].(map[string]any)["terminated"].(map[string]any)["exitCode"] = 128
	status["lastState"].(map[string]any)["terminated"].(map[string]any)["message"] = "failed to create containerd task: container init was OOM-killed (memory limit too low?)"
	result, ok, err := resourcesInferrer{}.Infer(context.Background(), demandReader{pod: pod}, f, fix.Plan{Strategy: "resources"})
	if err != nil || !ok || result.Options.MemoryRequest != "64Mi" || result.Options.MemoryLimit != "96Mi" {
		t.Fatalf("runtime-reported startup OOM should produce workload-derived sizing, got %+v ok=%v err=%v", result, ok, err)
	}
}

func TestResourcesDeclaredDemandDeclinesRecoveredPod(t *testing.T) {
	f := demandFinding()
	pod := demandPod(t, `{"containers":[{"name":"app","image":"polinux/stress","args":["--vm","1","--vm-bytes","100M"],"resources":{"limits":{"memory":"20Mi"}}}]}`)
	status := pod["status"].(map[string]any)["containerStatuses"].([]any)[0].(map[string]any)
	status["ready"] = true
	status["state"] = map[string]any{"running": map[string]any{}}
	if _, ok, _ := (resourcesInferrer{}).Infer(context.Background(), demandReader{pod: pod}, f, fix.Plan{Strategy: "resources"}); ok {
		t.Fatal("historical OOM in recovered container must decline")
	}
}
