package networkpolicyfix

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/fixora/kubectl-fixora/internal/serviceprobe"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/types"
)

type verifyRuntime struct {
	*fakeReader
	namespaces                map[string]*corev1.Namespace
	policies                  map[string]*networkingv1.NetworkPolicy
	pods                      map[string]*corev1.Pod
	deleted                   []ObjectRef
	outcomes                  map[string]bool
	openAllowed, denyEnforced bool
	failure                   string
	createCalls               int
}

func newVerifyRuntime(t *testing.T) (*verifyRuntime, Plan) {
	t.Helper()
	reader, authority, _ := candidateFixture(t)
	plan, err := Prepare(context.Background(), reader, authority, authority.Target())
	if err != nil {
		t.Fatal(err)
	}
	return &verifyRuntime{fakeReader: reader, namespaces: map[string]*corev1.Namespace{}, policies: map[string]*networkingv1.NetworkPolicy{}, pods: map[string]*corev1.Pod{}, outcomes: map[string]bool{"baseline-open": true, "deny-control": false, "ingress-allow": true, "ingress-deny": false, "egress-allow": true, "egress-deny": false}, openAllowed: true, denyEnforced: true}, plan
}
func key(ns, name string) string { return ns + "/" + name }
func (r *verifyRuntime) AdmitPolicy(_ context.Context, p *networkingv1.NetworkPolicy) (*networkingv1.NetworkPolicy, error) {
	out := p.DeepCopy()
	if r.failure == "admission" {
		out.Spec.Ingress = nil
	}
	return out, nil
}
func (r *verifyRuntime) CreateNamespace(_ context.Context, n *corev1.Namespace) (*corev1.Namespace, error) {
	r.createCalls++
	out := n.DeepCopy()
	out.UID = types.UID("ns-" + n.Name)
	out.ResourceVersion = "1"
	if r.failure == "namespace-label-drift" && out.Labels["env"] == "prod" {
		out.Labels["env"] = "staging"
	}
	out.Labels["kubernetes.io/metadata.name"] = out.Name
	r.namespaces[out.Name] = out.DeepCopy()
	return out, nil
}
func (r *verifyRuntime) CreatePolicy(_ context.Context, p *networkingv1.NetworkPolicy) (*networkingv1.NetworkPolicy, error) {
	r.createCalls++
	out := p.DeepCopy()
	out.UID = types.UID("policy-" + p.Name)
	out.ResourceVersion = "1"
	if r.failure == "policy-admission" {
		out.Spec.Ingress = append(out.Spec.Ingress, networkingv1.NetworkPolicyIngressRule{})
	}
	r.policies[key(out.Namespace, out.Name)] = out.DeepCopy()
	return out, nil
}
func (r *verifyRuntime) CreatePod(_ context.Context, p *corev1.Pod) (*corev1.Pod, error) {
	r.createCalls++
	out := p.DeepCopy()
	out.UID = types.UID("pod-" + p.Name)
	out.ResourceVersion = "1"
	role := out.Annotations[runRoleAnnotation]
	if r.failure == "pod-security-drift" {
		out.Spec.SecurityContext = nil
	}
	if role == "server" {
		out.Status.Phase = corev1.PodRunning
		out.Status.PodIP = "10.99.0.10"
		out.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}
	} else {
		stage := out.Annotations[caseAnnotation]
		allowed, known := r.outcomes[stage]
		if !known {
			allowed = false
		}
		if stage == "baseline-open" {
			allowed = r.openAllowed
		}
		if stage == "deny-control" {
			allowed = !r.denyEnforced
		}
		phase := corev1.PodFailed
		exit := int32(1)
		if allowed {
			phase = corev1.PodSucceeded
			exit = 0
		}
		out.Status.Phase = phase
		out.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "probe", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: exit}}}}
	}
	r.pods[key(out.Namespace, out.Name)] = out.DeepCopy()
	if (r.failure == "partial-create" || r.failure == "unresolved-create") && r.createCalls == 4 {
		return nil, errors.New("write response lost")
	}
	return out.DeepCopy(), nil
}
func (r *verifyRuntime) ReadNamespace(_ context.Context, name string) (*corev1.Namespace, error) {
	n := r.namespaces[name]
	if n == nil {
		return nil, fmt.Errorf("not found")
	}
	return n.DeepCopy(), nil
}
func (r *verifyRuntime) ReadPolicy(_ context.Context, ns, name string) (*networkingv1.NetworkPolicy, error) {
	p := r.policies[key(ns, name)]
	if p == nil {
		return nil, fmt.Errorf("not found")
	}
	return p.DeepCopy(), nil
}
func (r *verifyRuntime) ReadPod(_ context.Context, ns, name string) (*corev1.Pod, error) {
	p := r.pods[key(ns, name)]
	if p == nil {
		return nil, fmt.Errorf("not found")
	}
	return p.DeepCopy(), nil
}
func (r *verifyRuntime) FindAttempt(_ context.Context, ns, name, marker string) ([]ObjectRef, error) {
	if r.failure == "unresolved-create" && r.createCalls == 4 {
		return nil, errors.New("inventory unavailable")
	}
	var refs []ObjectRef
	if ns == "" {
		if n := r.namespaces[name]; n != nil && n.Annotations[runMarkerAnnotation] == marker {
			refs = append(refs, ObjectRef{Kind: "Namespace", Name: n.Name, UID: string(n.UID), RunMarker: marker})
		}
		return refs, nil
	}
	if p := r.policies[key(ns, name)]; p != nil && p.Annotations[runMarkerAnnotation] == marker {
		refs = append(refs, ObjectRef{Kind: "NetworkPolicy", Namespace: ns, Name: name, UID: string(p.UID), RunMarker: marker})
	}
	if p := r.pods[key(ns, name)]; p != nil && p.Annotations[runMarkerAnnotation] == marker {
		refs = append(refs, ObjectRef{Kind: "Pod", Namespace: ns, Name: name, UID: string(p.UID), RunMarker: marker})
	}
	return refs, nil
}
func (r *verifyRuntime) PodLogs(_ context.Context, ns, name string, _ int64) ([]byte, error) {
	p := r.pods[key(ns, name)]
	if p == nil {
		return nil, fmt.Errorf("pod not found")
	}
	var req serviceprobe.Request
	if len(p.Spec.Containers) == 0 || len(p.Spec.Containers[0].Args) < 2 || json.Unmarshal([]byte(p.Spec.Containers[0].Args[1]), &req) != nil {
		return nil, fmt.Errorf("invalid request")
	}
	stage := p.Annotations[caseAnnotation]
	allowed := r.outcomes[stage]
	if stage == "baseline-open" {
		allowed = r.openAllowed
	}
	if stage == "deny-control" {
		allowed = !r.denyEnforced
	}
	result := serviceprobe.Result{Nonce: req.Nonce}
	for _, target := range req.Targets {
		result.Checks = append(result.Checks, serviceprobe.Check{Target: target, Success: allowed})
	}
	if r.failure == "bad-witness" {
		result.Nonce = "wrong"
	}
	raw, _ := json.Marshal(result)
	return append(raw, '\n'), nil
}
func (r *verifyRuntime) DeleteOwned(_ context.Context, ref ObjectRef) error {
	if r.failure == "cleanup" {
		return errors.New("synthetic cleanup conflict")
	}
	switch ref.Kind {
	case "Pod":
		p := r.pods[key(ref.Namespace, ref.Name)]
		if p == nil || string(p.UID) != ref.UID || p.Annotations[runMarkerAnnotation] != ref.RunMarker {
			return errors.New("pod ownership mismatch")
		}
		delete(r.pods, key(ref.Namespace, ref.Name))
	case "NetworkPolicy":
		p := r.policies[key(ref.Namespace, ref.Name)]
		if p == nil || string(p.UID) != ref.UID || p.Annotations[runMarkerAnnotation] != ref.RunMarker {
			return errors.New("policy ownership mismatch")
		}
		delete(r.policies, key(ref.Namespace, ref.Name))
	case "Namespace":
		n := r.namespaces[ref.Name]
		if n == nil || string(n.UID) != ref.UID || n.Annotations[runMarkerAnnotation] != ref.RunMarker {
			return errors.New("namespace ownership mismatch")
		}
		delete(r.namespaces, ref.Name)
	default:
		return errors.New("unsupported cleanup kind")
	}
	r.deleted = append(r.deleted, ref)
	return nil
}

func TestVerifyRequiresBaselineAndDenyControls(t *testing.T) {
	t.Run("open baseline", func(t *testing.T) {
		r, p := newVerifyRuntime(t)
		r.openAllowed = false
		v, err := Verify(context.Background(), r, p)
		if err == nil || v.Receipt != nil {
			t.Fatalf("failed CNI baseline certified: %+v %v", v, err)
		}
	})
	t.Run("deny control", func(t *testing.T) {
		r, p := newVerifyRuntime(t)
		r.denyEnforced = false
		v, err := Verify(context.Background(), r, p)
		if err == nil || v.Receipt != nil {
			t.Fatalf("unenforced deny control certified: %+v %v", v, err)
		}
	})
}

func TestVerifyChecksIngressAndEgressMatrixAndCleans(t *testing.T) {
	r, p := newVerifyRuntime(t)
	v, err := Verify(context.Background(), r, p)
	if err != nil {
		t.Fatalf("verification=%+v err=%v", v, err)
	}
	if v.Status != "verified-policy-contract" || len(v.Cases) != 4 || v.Receipt == nil || len(r.namespaces) != 0 || len(r.pods) != 0 || len(r.policies) != 0 {
		t.Fatalf("verification incomplete: %+v leftovers ns=%d pod=%d policy=%d", v, len(r.namespaces), len(r.pods), len(r.policies))
	}
}

func TestVerifyWithholdsReceiptOnPolicyOrNamespaceAdmissionDrift(t *testing.T) {
	for _, failure := range []string{"policy-admission", "namespace-label-drift"} {
		t.Run(failure, func(t *testing.T) {
			r, p := newVerifyRuntime(t)
			r.failure = failure
			v, err := Verify(context.Background(), r, p)
			if err == nil || v.Receipt != nil {
				t.Fatalf("admission drift certified: %+v %v", v, err)
			}
		})
	}
}

func TestVerifyRejectsProbePodSecurityMutation(t *testing.T) {
	r, p := newVerifyRuntime(t)
	r.failure = "pod-security-drift"
	v, err := Verify(context.Background(), r, p)
	if err == nil || v.Receipt != nil {
		t.Fatalf("mutated probe security context certified: %+v %v", v, err)
	}
}

func TestVerifyWithholdsReceiptOnBadWitnessOrCleanupFailure(t *testing.T) {
	for _, failure := range []string{"bad-witness", "cleanup"} {
		t.Run(failure, func(t *testing.T) {
			r, p := newVerifyRuntime(t)
			r.failure = failure
			v, err := Verify(context.Background(), r, p)
			if err == nil || v.Receipt != nil {
				t.Fatalf("unsafe verifier result certified: %+v %v", v, err)
			}
		})
	}
}

func TestVerifyRejectsChangedPlanAndCancellation(t *testing.T) {
	r, p := newVerifyRuntime(t)
	p.Candidate.Spec.PodSelector.MatchLabels["app"] = "mutated"
	if _, err := Verify(context.Background(), r, p); err == nil {
		t.Fatal("mutated plan accepted")
	}
	r, p = newVerifyRuntime(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	v, err := Verify(ctx, r, p)
	if err == nil || v.Receipt != nil {
		t.Fatalf("cancelled verifier certified: %+v %v", v, err)
	}
}

func TestVerifyUsesAnnotationsForRunMarkers(t *testing.T) {
	r, p := newVerifyRuntime(t)
	v, err := Verify(context.Background(), r, p)
	if err != nil {
		t.Fatalf("verification=%+v err=%v", v, err)
	}
	if v.Receipt == nil {
		t.Fatal("verification receipt missing")
	}
	for _, ref := range r.deleted {
		if ref.RunMarker == "" {
			t.Fatalf("cleanup reference lacks run marker: %+v", ref)
		}
	}
}

func TestVerifyRecoversPartialCreateBeforeCleanup(t *testing.T) {
	r, p := newVerifyRuntime(t)
	r.failure = "partial-create"
	v, err := Verify(context.Background(), r, p)
	if err == nil || v.Receipt != nil {
		t.Fatalf("partial create unexpectedly certified: %+v %v", v, err)
	}
	if len(r.namespaces) != 0 || len(r.pods) != 0 || len(r.policies) != 0 {
		t.Fatalf("partial resource leaked after recovery: ns=%d pods=%d policies=%d", len(r.namespaces), len(r.pods), len(r.policies))
	}
}

func TestVerifyUnresolvedCreateWithholdsReceipt(t *testing.T) {
	r, p := newVerifyRuntime(t)
	r.failure = "unresolved-create"
	v, err := Verify(context.Background(), r, p)
	if err == nil || v.Receipt != nil || v.Status != "cleanup-incomplete" {
		t.Fatalf("unresolved create did not fail closed: %+v %v", v, err)
	}
}
