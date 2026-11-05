package e2e

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fixora/kubectl-fixora/internal/analyzer"
	"github.com/fixora/kubectl-fixora/internal/hpafix"
	"github.com/fixora/kubectl-fixora/internal/kube"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
)

func TestHPAHarnessChecksPrerequisitesBeforeCreate(t *testing.T) {
	script, err := os.ReadFile(filepath.Join("..", "..", "hack", "e2e-hpa.sh"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(script)
	optIn := strings.Index(s, `if [[ "${FIXORA_E2E_HPA:-}" != "1" ]]`)
	create := strings.Index(s, "kind create cluster")
	if optIn < 0 || create < 0 || optIn > create {
		t.Fatal("HPA harness must require explicit opt-in before creating a cluster")
	}
}

func TestHPAHarnessRequiresDedicatedContext(t *testing.T) {
	script, err := os.ReadFile(filepath.Join("..", "..", "hack", "e2e-hpa.sh"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(script)
	for _, required := range []string{"fixora-hpa-e2e-", "kind get clusters", "Refusing to reuse", "--kubeconfig", `KUBECONFIG="$task_dir/kubeconfig"`, `kind delete cluster --name "$cluster"`} {
		if !strings.Contains(s, required) {
			t.Errorf("dedicated local cluster guard missing %q", required)
		}
	}
}

func TestHPARequestRepairOnDedicatedKind(t *testing.T) {
	if os.Getenv("FIXORA_E2E_HPA") != "1" {
		t.Skip("requires explicit opt-in via hack/e2e-hpa.sh")
	}
	cluster := os.Getenv("FIXORA_E2E_KIND_CLUSTER")
	if !strings.HasPrefix(cluster, "fixora-hpa-e2e-") {
		t.Fatal("refusing HPA mutation outside the dedicated local kind cluster")
	}
	contextName := "kind-" + cluster
	command := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("kubectl", append([]string{"--context", contextName}, args...)...)
		cmd.Env = append(os.Environ(), "KUBECONFIG="+os.Getenv("KUBECONFIG"))
		raw, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("kubectl %v: %v\n%s", args, err, raw)
		}
		return string(raw)
	}
	ns := fmt.Sprintf("fixora-hpa-%d", time.Now().UnixNano())
	command("create", "namespace", ns)
	t.Cleanup(func() {
		_ = exec.Command("kubectl", "--context", contextName, "delete", "namespace", ns, "--wait=false").Run()
	})
	for _, file := range []string{"deployment.yaml", "hpa.yaml"} {
		raw, err := os.ReadFile(filepath.Join("fixtures", "hpa-repair", file))
		if err != nil {
			t.Fatal(err)
		}
		manifest := strings.ReplaceAll(string(raw), "FIXORA_NAMESPACE", ns)
		cmd := exec.Command("kubectl", "--context", contextName, "apply", "-f", "-")
		cmd.Env = append(os.Environ(), "KUBECONFIG="+os.Getenv("KUBECONFIG"))
		cmd.Stdin = strings.NewReader(manifest)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("apply %s: %v %s", file, err, out)
		}
	}
	command("rollout", "status", "deployment/api", "-n", ns, "--timeout=180s")
	client, err := kube.NewRequiredTypedClient(contextName, "dedicated local HPA acceptance")
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := hpafix.NewClient(client)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	waitHPACondition(t, 45*time.Second, "metrics-server readings", func() bool { metrics, e := runtime.ListPodMetrics(ctx, ns); return e == nil && len(metrics) > 0 })
	policy := filepath.Join(t.TempDir(), "approved.yaml")
	body, err := os.ReadFile(filepath.Join("fixtures", "hpa-repair", "policy.yaml.tpl"))
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(policy, []byte(strings.ReplaceAll(string(body), "FIXORA_NAMESPACE", ns)), 0600); err != nil {
		t.Fatal(err)
	}
	authority, err := hpafix.LoadPolicy(policy)
	if err != nil {
		t.Fatal(err)
	}
	finding := analyzer.Finding{Namespace: ns, ResourceKind: "HorizontalPodAutoscaler", ResourceName: "api-scale", Status: "MissingResourceRequests"}
	waitHPACondition(t, 60*time.Second, "HPA controller status", func() bool {
		h, e := runtime.GetHPA(ctx, ns, "api-scale")
		if e != nil || h.Status.ObservedGeneration == nil || *h.Status.ObservedGeneration != h.Generation || h.Status.DesiredReplicas < 1 || h.Status.CurrentReplicas != h.Status.DesiredReplicas {
			return false
		}
		for _, condition := range h.Status.Conditions {
			if condition.Type == autoscalingv2.AbleToScale && condition.Status == corev1.ConditionTrue {
				return true
			}
		}
		return false
	})
	plan, err := hpafix.Prepare(ctx, runtime, authority, finding)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := hpafix.Verify(ctx, runtime, plan)
	if err != nil {
		t.Fatal(err)
	}
	applied, outcome, err := hpafix.Apply(ctx, runtime, plan, receipt)
	if err != nil {
		t.Fatalf("apply %+v: %v", outcome, err)
	}
	observed, err := hpafix.Observe(ctx, runtime, applied)
	if err != nil || !observed.WorkloadConverged || !observed.HPAHealthy {
		t.Fatalf("HPA repair did not converge: %+v %v", observed, err)
	}
	rollback, err := hpafix.Rollback(ctx, runtime, applied)
	if err != nil || !rollback.RolledBack {
		t.Fatalf("field-only rollback %+v: %v", rollback, err)
	}
	live, err := runtime.GetWorkload(ctx, ns, "Deployment", "api")
	if err != nil {
		t.Fatal(err)
	}
	containers := live["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)["containers"].([]any)
	resources := containers[0].(map[string]any)["resources"].(map[string]any)
	if requests, exists := resources["requests"].(map[string]any); exists && len(requests) != 0 {
		t.Fatalf("Fixora request remains after rollback: %#v", resources)
	}
}

func waitHPACondition(t *testing.T, timeout time.Duration, what string, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if check() {
			return
		}
		time.Sleep(time.Second)
	}
	t.Fatalf("timed out waiting for %s", what)
}
