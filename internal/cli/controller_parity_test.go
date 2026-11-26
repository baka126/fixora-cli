package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/fixora/kubectl-fixora/internal/analyzer"
	"github.com/fixora/kubectl-fixora/internal/fix"
	"github.com/fixora/kubectl-fixora/internal/kube"
	"github.com/fixora/kubectl-fixora/internal/shadow"
	"gopkg.in/yaml.v3"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAIDynamicRepairsAcrossControllerKinds(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "crane"), []byte("#!/bin/sh\nprintf '%s' '{\"manifests\":[{\"platform\":{\"os\":\"linux\",\"architecture\":\"amd64\"}}]}'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	families := map[string]map[string]any{
		"image":      {"containers": []any{map[string]any{"name": "app", "image": "busybox:1.36"}}},
		"resources":  {"containers": []any{map[string]any{"name": "app", "resources": map[string]any{"limits": map[string]any{"memory": "137Mi"}}}}},
		"env":        {"containers": []any{map[string]any{"name": "app", "env": []any{map[string]any{"name": "ENDPOINT", "valueFrom": map[string]any{"configMapKeyRef": map[string]any{"name": "settings", "key": "endpoint"}}}}}}},
		"probe":      {"containers": []any{map[string]any{"name": "app", "readinessProbe": map[string]any{"tcpSocket": map[string]any{"port": 9097}}}}},
		"filesystem": {"containers": []any{map[string]any{"name": "app", "volumeMounts": []any{map[string]any{"name": "scratch", "mountPath": "/var/cache/custom"}}}}, "volumes": []any{map[string]any{"name": "scratch", "emptyDir": map[string]any{}}}, "securityContext": map[string]any{"fsGroup": 1007}},
	}
	for _, kind := range []string{"Deployment", "StatefulSet", "DaemonSet", "ReplicaSet", "Job", "CronJob", "Pod"} {
		for strategy, spec := range families {
			t.Run(kind+"/"+strategy, func(t *testing.T) {
				live, _ := kube.WorkloadPatch(kind, "dynamic", "demo", map[string]any{"containers": []any{map[string]any{"name": "app", "image": "busybox:1.35"}}})
				patch, _ := kube.WorkloadPatch(kind, "dynamic", "demo", spec)
				data, _ := yaml.Marshal(patch)
				f := analyzer.Finding{ResourceKind: kind, ResourceName: "dynamic", Namespace: "demo", Status: "CrashLoopBackOff", Evidence: []analyzer.Evidence{{Label: "Node platform", Value: "linux/amd64"}, {Label: "Container image app", Value: "busybox:1.35"}}, AI: &analyzer.AIResult{PatchYAML: string(data), Confidence: 97}}
				got := selectAIRepair(context.Background(), parityAIReader{baselineReader: baselineReader{object: live}}, fix.BuildPlan(f), f, &bytes.Buffer{}, true)
				want := kind != "Job" && kind != "ReplicaSet" && (kind != "Pod" || strategy == "image") && !(kind == "DaemonSet" && strategy == "resources")
				if got.ApplyEligible != want || got.PatchYAML() == "" || got.Strategy != strategy {
					t.Fatalf("%s/%s: %+v", kind, strategy, got)
				}
			})
		}
	}
}

type parityAIReader struct{ baselineReader }

func (r parityAIReader) GetResource(ctx context.Context, ns, res string) (map[string]any, error) {
	if res == "configmap/settings" {
		return map[string]any{"metadata": map[string]any{"name": "settings", "namespace": ns}, "data": map[string]any{"endpoint": "database.demo.svc"}}, nil
	}
	return r.baselineReader.GetResource(ctx, ns, res)
}
func TestControllerPolicyCannotBeBypassedAtDelivery(t *testing.T) {
	for _, kind := range []string{"Job", "ReplicaSet", "Pod"} {
		t.Run(kind, func(t *testing.T) {
			p := fix.Plan{Resource: kind + "/app", Strategy: "resources", ApplyEligible: true, CanApply: true, Safe: true, Confidence: 100}
			var out, errs bytes.Buffer
			code := deliverVerifiedFix(context.Background(), &out, &errs, options{yes: true}, kube.Kubectl{}, analyzer.Finding{ResourceKind: kind}, p, shadow.Result{Verified: true}, "cluster", true)
			if code == 0 || !strings.Contains(errs.String(), "blocked") {
				t.Fatalf("%s: %d %s", kind, code, errs.String())
			}
		})
	}
}

func TestControllerDeliveryPinsValidatedVersionAndReportsLiveOutcome(t *testing.T) {
	dir := t.TempDir()
	calls := filepath.Join(dir, "calls")
	counter := filepath.Join(dir, "reads")
	object, _ := kube.WorkloadPatch("StatefulSet", "app", "ns", map[string]any{"containers": []any{map[string]any{"name": "app", "image": "busybox:1.36"}}})
	data, _ := yaml.Marshal(object)
	proposal := filepath.Join(dir, "patch.yaml")
	if err := os.WriteFile(proposal, data, 0600); err != nil {
		t.Fatal(err)
	}
	object["metadata"].(map[string]any)["uid"] = "u"
	object["metadata"].(map[string]any)["resourceVersion"] = "10"
	object["metadata"].(map[string]any)["generation"] = float64(2)
	object["status"] = map[string]any{"observedGeneration": float64(2), "readyReplicas": float64(1), "updatedReplicas": float64(1), "currentRevision": "r", "updateRevision": "r"}
	before, _ := json.Marshal(object)
	object["metadata"].(map[string]any)["resourceVersion"] = "11"
	after, _ := json.Marshal(object)
	script := fmt.Sprintf(`#!/bin/sh
printf '%%s\n' "$*" >> %q
case "$1" in
get)
 if [ -f %q ]; then printf '%%s' '%s'; else : > %q; printf '%%s' '%s'; fi
 ;;
apply)
 file=""
 while [ "$#" -gt 0 ]; do
  if [ "$1" = "-f" ]; then shift; file="$1"; fi
  shift
 done
 /usr/bin/grep -q '"resourceVersion":"10"' "$file" || exit 1
 /usr/bin/grep -q '"uid":"u"' "$file" || exit 1
 ;;
rollout) printf 'success' ;;
*) exit 1 ;;
esac
`, calls, counter, after, counter, before)
	if err := os.WriteFile(filepath.Join(dir, "kubectl"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	var out, errs bytes.Buffer
	p := fix.Plan{Resource: "StatefulSet/app", Namespace: "ns", Strategy: "image", TargetUID: "u", CanApply: true, ApplyEligible: true, Safe: true, Confidence: 95, PatchTemplate: string(data)}
	code := deliverVerifiedFix(context.Background(), &out, &errs, options{yes: true, outFile: proposal, output: "json"}, kube.Kubectl{}, analyzer.Finding{ResourceKind: "StatefulSet", ResourceName: "app", Namespace: "ns"}, p, shadow.Result{Verified: true}, shadow.DeliveryCluster, true)
	if code != 0 {
		t.Fatalf("%d %s", code, errs.String())
	}
	var result shadow.Result
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.LiveVerification != "rollout-healthy" {
		t.Fatalf("missing live outcome: %+v", result)
	}
	trace, _ := os.ReadFile(calls)
	if !strings.Contains(string(trace), "--dry-run=server") {
		t.Fatal("mandatory server dry-run omitted")
	}
	original, _ := os.ReadFile(proposal)
	if string(original) != string(data) {
		t.Fatal("portable patch changed")
	}
}
