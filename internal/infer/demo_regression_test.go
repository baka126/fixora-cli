package infer

import (
	"context"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/fixora/kubectl-fixora/internal/analyzer"
	"github.com/fixora/kubectl-fixora/internal/fix"
	"github.com/fixora/kubectl-fixora/internal/shadow"
	"gopkg.in/yaml.v3"
)

// Exercise the inference registry, planner and shadow policy together. A
// successful inferrer alone does not prove its patch is eligible or verifiable.
// These fixtures use public synthetic evidence and need no cluster or provider.
func TestDemoRepairRegression(t *testing.T) {
	type scenario struct {
		name, strategy, container string
		setup                     func(*testing.T) (kubeReader, analyzer.Finding)
		fields                    map[string]any
	}
	cases := []scenario{
		{"imagepull", "image", "typo-container", func(t *testing.T) (kubeReader, analyzer.Finding) {
			f := imageFinding("docker.io/library/nginx@sha256:"+strings.Repeat("a", 64), 90)
			f.Status = "ImagePullBackOff"
			return nil, f
		}, map[string]any{"containers.0.image": "docker.io/library/nginx@sha256:" + strings.Repeat("a", 64)}},
		{"pending", "resources", "greedy-container", func(t *testing.T) (kubeReader, analyzer.Finding) {
			return nil, allocatableFinding("requested memory=102400Mi cpu=100000m; largest node allocatable memory=7936Mi cpu=8000m")
		}, map[string]any{"containers.0.resources.requests.memory": "3968Mi", "containers.0.resources.limits.memory": "3968Mi", "containers.0.resources.requests.cpu": "4000m"}},
		{"oomkilled", "resources", "app", func(t *testing.T) (kubeReader, analyzer.Finding) {
			return demandReader{pod: demandPod(t, `{"containers":[{"name":"app","image":"polinux/stress","args":["--vm","1","--vm-bytes","100M","--vm-hang","1"],"resources":{"limits":{"memory":"20Mi"}}}]}`)}, demandFinding()
		}, map[string]any{"containers.0.resources.requests.memory": "112Mi", "containers.0.resources.limits.memory": "160Mi"}},
		{"probe", "probe", "web-app", func(t *testing.T) (kubeReader, analyzer.Finding) {
			return nil, analyzer.Finding{Status: "ProbeFailure", Evidence: []analyzer.Evidence{
				{Label: "Container image web-app", Value: "nginx:1.25"},
				{Label: "Container ports", Value: "8080"},
				{Label: "Readiness probe port", Value: "80"},
			}}
		}, map[string]any{"containers.0.readinessProbe.httpGet.port": 8080}},
		{"security", "filesystem", "worker", func(t *testing.T) (kubeReader, analyzer.Finding) {
			return filesystemReader{pod: filesystemPod("if ! touch /var/cache/app.tmp; then echo 'permission denied: cannot write cache'; exit 1; fi; sleep 3600")}, analyzer.Finding{
				ResourceName: "worker", Namespace: "demo", PodName: "worker-pod", Status: "PermissionDenied",
				Logs: []analyzer.LogSnippet{{Text: "permission denied: cannot write cache"}},
			}
		}, map[string]any{"containers.0.volumeMounts.0.mountPath": "/var/cache", "securityContext.fsGroup": 1000, "volumes.0.emptyDir": map[string]any{}}},
		{"dependency", "env", "client", func(t *testing.T) (kubeReader, analyzer.Finding) {
			r, f := depFixture(t)
			return r, f
		}, map[string]any{"containers.0.env.0.value": nil, "containers.0.env.0.valueFrom.configMapKeyRef.name": "connection", "containers.0.env.0.valueFrom.configMapKeyRef.key": "DB_HOST", "containers.0.env.1.valueFrom.configMapKeyRef.key": "DB_PORT"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, f := tc.setup(t)
			f.ResourceKind = "Deployment"
			if f.ResourceName == "" {
				f.ResourceName = tc.name + "-demo"
			}
			if f.Namespace == "" {
				f.Namespace = "demo"
			}
			p, ok := Concrete(context.Background(), r, f, fix.BuildPlan(f))
			if !ok || !p.ApplyEligible || p.Strategy != tc.strategy {
				t.Fatalf("repair lost: strategy=%s eligible=%v ok=%v blocked=%v", p.Strategy, p.ApplyEligible, ok, p.BlockedReasons)
			}
			if err := shadow.ValidateCandidatePatch(p.PatchYAML(), p.Strategy, p.Resource, p.Namespace); err != nil {
				t.Fatalf("generated repair cannot pass shadow policy: %v\n%s", err, p.PatchYAML())
			}
			var patch map[string]any
			if err := yaml.Unmarshal([]byte(p.PatchYAML()), &patch); err != nil {
				t.Fatal(err)
			}
			pod, ok := regressionField(patch, "spec.template.spec")
			if !ok {
				t.Fatal("repair is not directed at the Deployment pod template")
			}
			fields := map[string]any{"containers.0.name": tc.container}
			for path, want := range tc.fields {
				fields[path] = want
			}
			for path, want := range fields {
				got, exists := regressionField(pod, path)
				if !exists || !reflect.DeepEqual(got, want) {
					t.Errorf("%s: got %#v (present=%v), want %#v", path, got, exists, want)
				}
			}
			// These repairs must not conceal failures by replacing the process
			// or relaxing existing container security settings.
			for _, path := range []string{"containers.0.command", "containers.0.args", "containers.0.securityContext"} {
				if _, exists := regressionField(pod, path); exists {
					t.Errorf("unrelated change at %s", path)
				}
			}
			if !strings.Contains(p.RollbackCommand, "rollout undo deployment/"+f.ResourceName) {
				t.Error("lost Deployment rollback advice")
			}
			f.Recovered = true
			if recovered, ok := Concrete(context.Background(), r, f, fix.BuildPlan(f)); ok || recovered.ApplyEligible {
				t.Error("recovered workload must not receive this repair")
			}
		})
	}
}

func TestDemoMissingSecretRequiresOperatorValue(t *testing.T) {
	f := envFinding("Secret", "missing-secret")
	f.ResourceKind, f.ResourceName, f.Namespace = "Deployment", "missing-config-demo", "demo"
	r := fakeConfigReader{items: []map[string]any{configMap("missing-secrets", "some-key")}}
	p, ok := Concrete(context.Background(), r, f, fix.BuildPlan(f))
	if ok || p.ApplyEligible {
		t.Fatal("missing Secret must not be repaired with invented values or an unrelated ConfigMap")
	}
}

func TestDemoUnknownStartupHasNoInventedRepair(t *testing.T) {
	f := analyzer.Finding{ResourceKind: "Deployment", ResourceName: "crashloop-demo", Namespace: "demo", Status: "CrashLoopBackOff"}
	p, ok := Concrete(context.Background(), nil, f, fix.BuildPlan(f))
	if ok || p.ApplyEligible || p.Strategy != "runtime" {
		t.Fatalf("unproven startup must stay review-only: %+v", p)
	}
}

func regressionField(root any, path string) (any, bool) {
	for _, part := range strings.Split(path, ".") {
		switch obj := root.(type) {
		case map[string]any:
			var ok bool
			root, ok = obj[part]
			if !ok {
				return nil, false
			}
		case []any:
			i, err := strconv.Atoi(part)
			if err != nil || i < 0 || i >= len(obj) {
				return nil, false
			}
			root = obj[i]
		default:
			return nil, false
		}
	}
	return root, true
}
