package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/fixora/kubectl-fixora/internal/analyzer"
	"github.com/fixora/kubectl-fixora/internal/kube"
	"github.com/fixora/kubectl-fixora/internal/pdbfix"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	kfake "k8s.io/client-go/kubernetes/fake"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const cliPDBPolicy = `apiVersion: fixora.io/v1alpha1
kind: PDBRepairPolicy
metadata: {name: approve, namespace: team}
spec:
  pdb: {name: protect}
  workload: {kind: Deployment, name: workers}
  selector: {matchLabels: {role: api}}
  budget: {minAvailable: 2}
  availability: {minimumHealthyReplicas: 2, maximumConcurrentDisruptions: 1}
`

func cliPDBFixture(t *testing.T) (options, pdbfix.Runtime, *kfake.Clientset, analyzer.Finding) {
	t.Helper()
	file := filepath.Join(t.TempDir(), "policy.yaml")
	os.WriteFile(file, []byte(cliPDBPolicy), 0600)
	n := int32(3)
	yes := true
	v := intstr.FromInt32(3)
	d := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "workers", Namespace: "team", UID: "deployment", Generation: 1}, Spec: appsv1.DeploymentSpec{Replicas: &n, Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"role": "api"}}}, Status: appsv1.DeploymentStatus{ObservedGeneration: 1, Replicas: 3, UpdatedReplicas: 3, AvailableReplicas: 3, ReadyReplicas: 3}}
	rs := &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: "revision", Namespace: "team", UID: "rs", OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "Deployment", Name: "workers", UID: "deployment", Controller: &yes}}}}
	p := &policyv1.PodDisruptionBudget{ObjectMeta: metav1.ObjectMeta{Name: "protect", Namespace: "team", UID: "budget", ResourceVersion: "1", Generation: 1}, Spec: policyv1.PodDisruptionBudgetSpec{Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"role": "api"}}, MinAvailable: &v}, Status: policyv1.PodDisruptionBudgetStatus{ObservedGeneration: 1, CurrentHealthy: 3, DesiredHealthy: 3, ExpectedPods: 3}}
	objs := []runtime.Object{d, rs, p}
	for _, name := range []string{"a", "b", "c"} {
		objs = append(objs, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "team", UID: types.UID(name), Labels: map[string]string{"role": "api"}, OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: "revision", UID: "rs", Controller: &yes}}}, Status: corev1.PodStatus{Phase: corev1.PodRunning, Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}}})
	}
	client := kfake.NewSimpleClientset(objs...)
	r, e := pdbfix.NewClient(&kube.TypedClient{Clientset: client})
	if e != nil {
		t.Fatal(e)
	}
	return options{output: "text", pdbPolicy: file, preview: true, timeout: time.Second, delivery: "patch", visited: map[string]bool{}}, r, client, analyzer.Finding{ResourceKind: "PodDisruptionBudget", ResourceName: "protect", Namespace: "team", Status: "DisruptionProtectionActive", Summary: "Healthy replicas are protected."}
}
func assertNoPDBWrites(t *testing.T, c *kfake.Clientset) {
	t.Helper()
	for _, a := range c.Actions() {
		if a.GetVerb() != "get" && a.GetVerb() != "list" {
			t.Fatal("unexpected mutation", a)
		}
	}
}
func TestPDBCLIOutputAndConsent(t *testing.T) {
	for _, format := range []string{"text", "json", "yaml"} {
		t.Run(format, func(t *testing.T) {
			o, r, c, f := cliPDBFixture(t)
			o.output = format
			o.apply = true
			o.delivery = "pr"
			o.outFile = filepath.Join(t.TempDir(), "must-not-exist")
			var out, err bytes.Buffer
			if code := runPDBFixRuntime(context.Background(), &out, &err, o, r, f); code != 0 {
				t.Fatalf("%d %s", code, err.String())
			}
			assertNoPDBWrites(t, c)
			if _, e := os.Stat(o.outFile); !os.IsNotExist(e) {
				t.Fatal("preview wrote file")
			}
			if format == "json" && !json.Valid(out.Bytes()) {
				t.Fatal("invalid JSON", out.String())
			}
			if strings.Contains(out.String(), "receipt") {
				t.Fatal("serialized authority")
			}
		})
	}
	o, _, _, f := cliPDBFixture(t)
	o.pdbPolicy = ""
	f.AI = &analyzer.AIResult{RootCause: "The budget intentionally protects all healthy replicas."}
	var out, err bytes.Buffer
	if code := runPDBFixRuntime(context.Background(), &out, &err, o, nil, f); code != 0 || !strings.Contains(out.String(), "AI:") || strings.Contains(out.String(), "Suggested diff") {
		t.Fatalf("missing concise AI output %s %s", out.String(), err.String())
	}
}
func TestPDBCLIProposalIsolation(t *testing.T) {
	o, r, c, f := cliPDBFixture(t)
	enriched, e := withPDBProposalIntent(f, o.pdbPolicy)
	if e != nil || len(enriched.Evidence) == 0 {
		t.Fatal(enriched, e)
	}
	f.ResourceName = "wrong"
	if _, e = withPDBProposalIntent(f, o.pdbPolicy); e == nil {
		t.Fatal("mismatched policy")
	}
	f.ResourceName = "protect"
	o.servicePolicy = "wrong"
	var out, err bytes.Buffer
	if runPDBFixRuntime(context.Background(), &out, &err, o, r, f) != 2 {
		t.Fatal("mixed policy accepted")
	}
	assertNoPDBWrites(t, c)
}
func TestPDBCLIDeliveryModes(t *testing.T) {
	o, r, c, f := cliPDBFixture(t)
	o.preview = false
	var out, err bytes.Buffer
	if code := runPDBFixRuntime(context.Background(), &out, &err, o, r, f); code != 0 {
		t.Fatal(code, err.String())
	}
	assertNoPDBWrites(t, c)
	if !strings.Contains(out.String(), "review") || strings.Contains(out.String(), "shadow verified") {
		t.Fatal(out.String())
	}
	o.visited["timeout"] = true
	o.timeout = 0
	if runPDBFixRuntime(context.Background(), &out, &err, o, r, f) != 2 {
		t.Fatal("invalid timeout accepted")
	}
}
func TestPDBSourceReviewIsolation(t *testing.T) {
	o, r, c, f := cliPDBFixture(t)
	o.preview = false
	o.delivery = "pr"
	o.repoPath = t.TempDir()
	os.WriteFile(filepath.Join(o.repoPath, "Chart.yaml"), []byte("apiVersion: v2\nname: example\nversion: 1.0.0\n"), 0600)
	var out, err bytes.Buffer
	if code := runPDBFixRuntime(context.Background(), &out, &err, o, r, f); code != 0 || !strings.Contains(out.String(), "manual") {
		t.Fatalf("helm path: %d %s %s", code, out.String(), err.String())
	}
	assertNoPDBWrites(t, c)
}
