package pdbfix

import (
	"encoding/json"
	policyv1 "k8s.io/api/policy/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const validPolicy = `apiVersion: fixora.io/v1alpha1
kind: PDBRepairPolicy
metadata: {name: approval, namespace: team}
spec:
  pdb: {name: protect}
  workload: {kind: Deployment, name: backend}
  selector: {matchLabels: {role: api}}
  budget: {minAvailable: 2}
  availability: {minimumHealthyReplicas: 2, maximumConcurrentDisruptions: 1}
`

func loadText(t *testing.T, s string) (*Authority, error) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "policy.yaml")
	if err := os.WriteFile(p, []byte(s), 0600); err != nil {
		t.Fatal(err)
	}
	return LoadPolicy(p)
}
func TestPDBPolicyStrictIntent(t *testing.T) {
	for _, tc := range []struct {
		name     string
		old, new string
	}{
		{"unknown", "spec:", "unexpected: true\nspec:"}, {"duplicate", "budget: {minAvailable: 2}", "budget: {minAvailable: 2, minAvailable: 1}"}, {"extra doc", "", "---\n"}, {"missing ceiling", ", maximumConcurrentDisruptions: 1", ""}, {"missing floor", "minimumHealthyReplicas: 2, ", ""}, {"zero floor", "minimumHealthyReplicas: 2", "minimumHealthyReplicas: 0"}, {"negative ceiling", "maximumConcurrentDisruptions: 1", "maximumConcurrentDisruptions: -1"}, {"bad name", "name: protect", "name: INVALID"}, {"bad kind", "kind: Deployment", "kind: DaemonSet"}, {"empty selector", "selector: {matchLabels: {role: api}}", "selector: {}"}, {"both budget", "budget: {minAvailable: 2}", "budget: {minAvailable: 2, maxUnavailable: 1}"}, {"no budget", "budget: {minAvailable: 2}", "budget: {}"}, {"numeric string", "minAvailable: 2", "minAvailable: \"2\""}, {"invalid percent", "minAvailable: 2", "minAvailable: \"101%\""}, {"negative", "minAvailable: 2", "minAvailable: -1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := strings.Replace(validPolicy, tc.old, tc.new, 1)
			if tc.name == "extra doc" {
				s = validPolicy + tc.new
			}
			if _, err := loadText(t, s); err == nil {
				t.Fatal("accepted invalid intent")
			}
		})
	}
	for _, s := range []string{validPolicy, strings.Replace(validPolicy, "minAvailable: 2", "minAvailable: \"50%\"", 1), strings.Replace(validPolicy, "maximumConcurrentDisruptions: 1", "maximumConcurrentDisruptions: 0", 1)} {
		a, e := loadText(t, s)
		if e != nil {
			t.Fatal(e)
		}
		if a.Target().Name != "backend" {
			t.Fatal("wrong target")
		}
		if _, e = a.ProposalContext("other", "protect"); e == nil {
			t.Fatal("wrong target got context")
		}
	}
}
func TestPDBBudgetRoundingAndBounds(t *testing.T) {
	for _, tc := range []struct {
		min                             bool
		value                           intstr.IntOrString
		n, h, wantRequired, wantAllowed int32
	}{
		{true, intstr.FromString("50%"), 3, 3, 2, 1}, {false, intstr.FromString("50%"), 3, 3, 1, 2}, {true, intstr.FromString("50%"), 1, 1, 1, 0}, {false, intstr.FromString("50%"), 1, 1, 0, 1}, {true, intstr.FromInt32(4), 3, 3, 4, 0}, {false, intstr.FromInt32(9), 3, 3, 0, 3}, {true, intstr.FromInt32(2), 3, 1, 2, 0},
	} {
		spec := policyv1.PodDisruptionBudgetSpec{}
		if tc.min {
			spec.MinAvailable = &tc.value
		} else {
			spec.MaxUnavailable = &tc.value
		}
		got, e := EvaluateBudget(spec, tc.n, tc.h)
		if e != nil || got.RequiredHealthy != tc.wantRequired || got.AllowedDisruptions != tc.wantAllowed {
			t.Fatalf("%+v: %+v %v", tc, got, e)
		}
	}
	for _, v := range []intstr.IntOrString{intstr.FromInt32(-1), intstr.FromString("101%"), intstr.FromString("2"), intstr.FromString("9999999999999999999999%")} {
		_, e := EvaluateBudget(policyv1.PodDisruptionBudgetSpec{MinAvailable: &v}, 3, 3)
		if e == nil {
			t.Fatal("accepted bad value", v)
		}
	}
	v := intstr.FromInt32(1)
	if _, e := EvaluateBudget(policyv1.PodDisruptionBudgetSpec{MinAvailable: &v}, 0, 0); e == nil {
		t.Fatal("accepted zero replicas")
	}
}
func TestPDBAuthorityCannotBeReconstituted(t *testing.T) {
	a, e := loadText(t, validPolicy)
	if e != nil {
		t.Fatal(e)
	}
	b, _ := json.Marshal(a)
	var copy Authority
	json.Unmarshal(b, &copy)
	if _, e = copy.ProposalContext("team", "protect"); e == nil {
		t.Fatal("roundtrip created authority")
	}
	if _, e = (&Authority{}).ProposalContext("team", "protect"); e == nil {
		t.Fatal("zero authority accepted")
	}
	if e = os.WriteFile(a.path, []byte(strings.Replace(validPolicy, "minAvailable: 2", "minAvailable: 1", 1)), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e = a.ProposalContext("team", "protect"); e == nil {
		t.Fatal("changed bytes retained approval")
	}
}
