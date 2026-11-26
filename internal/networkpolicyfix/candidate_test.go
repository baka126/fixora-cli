package networkpolicyfix

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"

	"github.com/fixora/kubectl-fixora/internal/kube"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type fakeReader struct {
	policy                                                               *networkingv1.NetworkPolicy
	policies                                                             []networkingv1.NetworkPolicy
	pods                                                                 []corev1.Pod
	namespace                                                            *corev1.Namespace
	workload                                                             map[string]any
	owners                                                               map[string]kube.WorkloadOwner
	policyErr, policiesErr, podsErr, namespaceErr, workloadErr, ownerErr error
}

func (r *fakeReader) GetPolicy(context.Context, string, string) (*networkingv1.NetworkPolicy, error) {
	if r.policyErr != nil {
		return nil, r.policyErr
	}
	if r.policy == nil {
		return nil, nil
	}
	return r.policy.DeepCopy(), nil
}
func (r *fakeReader) ListPolicies(context.Context, string) ([]networkingv1.NetworkPolicy, error) {
	if r.policiesErr != nil {
		return nil, r.policiesErr
	}
	out := make([]networkingv1.NetworkPolicy, len(r.policies))
	for i := range r.policies {
		r.policies[i].DeepCopyInto(&out[i])
	}
	return out, nil
}
func (r *fakeReader) ListPods(context.Context, string) ([]corev1.Pod, error) {
	if r.podsErr != nil {
		return nil, r.podsErr
	}
	out := make([]corev1.Pod, len(r.pods))
	for i := range r.pods {
		r.pods[i].DeepCopyInto(&out[i])
	}
	return out, nil
}
func (r *fakeReader) GetNamespace(context.Context, string) (*corev1.Namespace, error) {
	if r.namespaceErr != nil {
		return nil, r.namespaceErr
	}
	if r.namespace == nil {
		return nil, nil
	}
	return r.namespace.DeepCopy(), nil
}
func (r *fakeReader) GetWorkload(context.Context, string, string, string) (map[string]any, error) {
	if r.workloadErr != nil {
		return nil, r.workloadErr
	}
	raw, _ := json.Marshal(r.workload)
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return out, nil
}
func (r *fakeReader) ResolveOwner(_ context.Context, pod corev1.Pod) (kube.WorkloadOwner, error) {
	if r.ownerErr != nil {
		return kube.WorkloadOwner{}, r.ownerErr
	}
	owner, ok := r.owners[string(pod.UID)]
	if !ok {
		return kube.WorkloadOwner{}, fmt.Errorf("owner unavailable")
	}
	return owner, nil
}

func candidateFixture(t *testing.T) (*fakeReader, *Authority, string) {
	t.Helper()
	path := writeAuthority(t, validAuthorityYAML)
	a, err := LoadAuthority(path)
	if err != nil {
		t.Fatal(err)
	}
	old := networkingv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{Name: "db-policy", Namespace: "fixora-demo", UID: "policy-uid", ResourceVersion: "7", Labels: map[string]string{"team": "database"}, Annotations: map[string]string{"note": "preserve"}}, Spec: networkingv1.NetworkPolicySpec{PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{"old": "selector"}}, PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress}}}
	policy := old.DeepCopy()
	workload := map[string]any{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": map[string]any{"name": "database", "namespace": "fixora-demo", "uid": "deployment-uid", "resourceVersion": "11", "generation": 3}, "spec": map[string]any{"replicas": 1, "selector": map[string]any{"matchLabels": map[string]any{"app": "db"}}}, "status": map[string]any{"observedGeneration": 3, "replicas": 1, "updatedReplicas": 1, "readyReplicas": 1, "availableReplicas": 1}}
	pod := corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "database-0", Namespace: "fixora-demo", UID: "pod-uid", Labels: map[string]string{"app": "db"}}, Status: corev1.PodStatus{Phase: corev1.PodRunning, Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}}}
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "fixora-demo", UID: "ns-uid", ResourceVersion: "2", Labels: map[string]string{"team": "demo"}}}
	owner := kube.WorkloadOwner{Kind: "Deployment", Name: "database", UID: "deployment-uid", Identities: []kube.OwnerIdentity{{Kind: "Deployment", Name: "database", UID: "deployment-uid"}}, Object: workload}
	r := &fakeReader{policy: policy, policies: []networkingv1.NetworkPolicy{old}, pods: []corev1.Pod{pod}, namespace: ns, workload: workload, owners: map[string]kube.WorkloadOwner{"pod-uid": owner}}
	return r, a, path
}

func TestCollectPreservesUnreadableInventories(t *testing.T) {
	r, a, _ := candidateFixture(t)
	r.podsErr = fmt.Errorf("forbidden")
	s, err := Collect(context.Background(), r, "fixora-demo", "db-policy")
	if err != nil || !hasIssue(s.Issues, "PodInventoryUnavailable") || len(s.Pods) != 0 {
		t.Fatalf("incomplete reads were not preserved: %+v err=%v", s, err)
	}
	if _, err := Prepare(context.Background(), r, a, a.Target()); err == nil {
		t.Fatal("nil authority bypassed incomplete inventory refusal")
	}
}

func TestPrepareCreatesExactSpecCandidateAndPreservesMetadata(t *testing.T) {
	r, a, _ := candidateFixture(t)
	plan, err := Prepare(context.Background(), r, a, a.Target())
	if err != nil {
		t.Fatal(err)
	}
	if plan.SelectedPodCount != 1 || plan.Candidate.Spec.PodSelector.MatchLabels["app"] != "db" || plan.Candidate.Labels["team"] != "database" || plan.Candidate.Annotations["note"] != "preserve" {
		t.Fatalf("candidate=%+v", plan)
	}
	if err := plan.CheckBound(); err != nil {
		t.Fatal(err)
	}
	plan.Candidate.Spec.PodSelector.MatchLabels["app"] = "changed"
	if err := plan.CheckBound(); err == nil {
		t.Fatal("mutated public plan retained authority")
	}
}

func TestPrepareRefusesIncompleteAndAmbiguousEvidence(t *testing.T) {
	cases := map[string]func(*fakeReader){
		"pod inventory unavailable":       func(r *fakeReader) { r.podsErr = fmt.Errorf("forbidden") },
		"policy inventory unavailable":    func(r *fakeReader) { r.policiesErr = fmt.Errorf("forbidden") },
		"missing policy uid":              func(r *fakeReader) { r.policy.UID = ""; r.policies[0].UID = "" },
		"missing policy resource version": func(r *fakeReader) { r.policy.ResourceVersion = ""; r.policies[0].ResourceVersion = "" },
		"duplicate target inventory":      func(r *fakeReader) { r.policies = append(r.policies, r.policies[0]) },
		"source-managed policy": func(r *fakeReader) {
			r.policy.Annotations["argocd.argoproj.io/tracking-id"] = "app"
			r.policies[0].Annotations = r.policy.Annotations
		},
		"unsupported owner": func(r *fakeReader) {
			r.owners["pod-uid"] = kube.WorkloadOwner{Kind: "Job", Name: "other", UID: "other"}
		},
		"unready target pod": func(r *fakeReader) { r.pods[0].Status.Phase = corev1.PodPending },
		"target selector includes unrelated pod": func(r *fakeReader) {
			pod := r.pods[0].DeepCopy()
			pod.UID = "other-pod"
			pod.Name = "other"
			r.pods = append(r.pods, *pod)
		},
		"other policy selects target pod": func(r *fakeReader) {
			other := networkingv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{Name: "other", Namespace: "fixora-demo", UID: "other-policy"}, Spec: networkingv1.NetworkPolicySpec{PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{"app": "db"}}}}
			r.policies = append(r.policies, other)
		},
		"source-managed workload": func(r *fakeReader) {
			metadata := r.workload["metadata"].(map[string]any)
			metadata["annotations"] = map[string]any{"meta.helm.sh/release-name": "demo"}
		},
		"workload generation stale":  func(r *fakeReader) { r.workload["status"].(map[string]any)["observedGeneration"] = 2 },
		"workload pod set mismatch":  func(r *fakeReader) { r.workload["spec"].(map[string]any)["replicas"] = 2 },
		"namespace identity missing": func(r *fakeReader) { r.namespace.UID = "" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			r, a, _ := candidateFixture(t)
			mutate(r)
			if _, err := Prepare(context.Background(), r, a, a.Target()); err == nil {
				t.Fatal("unsafe candidate accepted")
			}
		})
	}
}

func TestPrepareRejectsChangedAuthorityAndTargetMismatch(t *testing.T) {
	r, a, path := candidateFixture(t)
	if _, err := Prepare(context.Background(), r, a, Target{Namespace: "fixora-demo", Name: "other"}); err == nil {
		t.Fatal("mismatched CLI target accepted")
	}
	if err := os.WriteFile(path, []byte(validAuthorityYAML+"\n# changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Prepare(context.Background(), r, a, a.Target()); err == nil {
		t.Fatal("changed local authority accepted")
	}
}
