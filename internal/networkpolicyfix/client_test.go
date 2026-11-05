package networkpolicyfix

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/fixora/kubectl-fixora/internal/kube"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	ktesting "k8s.io/client-go/testing"
)

func testTypedClient(objects ...runtime.Object) *kube.TypedClient {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = networkingv1.AddToScheme(scheme)
	return &kube.TypedClient{Clientset: fake.NewSimpleClientset(objects...), Dynamic: dynamicfake.NewSimpleDynamicClient(scheme), Config: &rest.Config{}}
}

type deliveryRuntime struct {
	*verifyRuntime
	patches int
	lost    bool
}

func (r *deliveryRuntime) PatchSpec(_ context.Context, ns, name, uid, rv string, spec networkingv1.NetworkPolicySpec, _ bool) (*networkingv1.NetworkPolicy, error) {
	if r.fakeReader.policy == nil || string(r.fakeReader.policy.UID) != uid || r.fakeReader.policy.ResourceVersion != rv {
		return nil, fmt.Errorf("precondition conflict")
	}
	if r.lost {
		r.fakeReader.policy.Spec = *spec.DeepCopy()
		r.fakeReader.policy.ResourceVersion = "rv-uncertain"
		return nil, fmt.Errorf("response lost")
	}
	r.patches++
	r.fakeReader.policy.Spec = *spec.DeepCopy()
	r.fakeReader.policy.ResourceVersion = fmt.Sprintf("rv-%d", r.patches+10)
	for i := range r.fakeReader.policies {
		if r.fakeReader.policies[i].Name == name {
			r.fakeReader.policies[i] = *r.fakeReader.policy.DeepCopy()
		}
	}
	return r.fakeReader.policy.DeepCopy(), nil
}

func TestApplyConsumesReceiptAndRestoreRequiresSeparateConsent(t *testing.T) {
	vr, plan := newVerifyRuntime(t)
	rt := &deliveryRuntime{verifyRuntime: vr}
	verification, err := Verify(context.Background(), rt, plan)
	if err != nil {
		t.Fatal(err)
	}
	applied, err := Apply(context.Background(), rt, plan, verification.Receipt)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = Apply(context.Background(), rt, plan, verification.Receipt); err == nil {
		t.Fatal("receipt replay accepted")
	}
	out, err := Observe(context.Background(), rt, applied)
	if err != nil || out.Status != "policy-spec-observed" {
		t.Fatalf("observe=%+v err=%v", out, err)
	}
	if _, err = Restore(context.Background(), rt, applied, false); err == nil {
		t.Fatal("rollback without separate consent accepted")
	}
	out, err = Restore(context.Background(), rt, applied, true)
	if err != nil || out.Status != "original-policy-spec-restored" {
		t.Fatalf("restore=%+v err=%v", out, err)
	}
	if _, err = Restore(context.Background(), rt, applied, true); err == nil {
		t.Fatal("rollback replay accepted")
	}
}

func TestApplyLostResponseIsUncertainAndNeverRetries(t *testing.T) {
	vr, plan := newVerifyRuntime(t)
	rt := &deliveryRuntime{verifyRuntime: vr, lost: true}
	verification, err := Verify(context.Background(), rt, plan)
	if err != nil {
		t.Fatal(err)
	}
	applied, err := Apply(context.Background(), rt, plan, verification.Receipt)
	if err != nil || applied == nil || applied.Status != "apply-response-recovered" {
		t.Fatalf("lost response was not resolved by readback: applied=%+v err=%v", applied, err)
	}
	if rt.patches != 0 {
		t.Fatal("uncertain patch was retried")
	}
}

func TestRestoreResolvesLostWriteByReadback(t *testing.T) {
	vr, plan := newVerifyRuntime(t)
	rt := &deliveryRuntime{verifyRuntime: vr}
	verification, err := Verify(context.Background(), rt, plan)
	if err != nil {
		t.Fatal(err)
	}
	applied, err := Apply(context.Background(), rt, plan, verification.Receipt)
	if err != nil {
		t.Fatal(err)
	}
	rt.lost = true
	out, err := Restore(context.Background(), rt, applied, true)
	if err != nil || out.Status != "original-policy-spec-restored" {
		t.Fatalf("rollback lost response not resolved: %+v %v", out, err)
	}
}

func TestClientRequiresCompleteTypedStack(t *testing.T) {
	if _, err := NewClient(&kube.TypedClient{}); err == nil {
		t.Fatal("kubectl fallback accepted for policy mutation")
	}
}

func TestPatchSpecUsesUIDResourceVersionTestsAndOnlyReplacesSpec(t *testing.T) {
	old := &networkingv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{Name: "p", Namespace: "n", UID: types.UID("uid-1"), ResourceVersion: "rv-2"}, Spec: networkingv1.NetworkPolicySpec{PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{"old": "yes"}}}}
	client := testTypedClient(old)
	cs := client.Clientset.(*fake.Clientset)
	cs.PrependReactor("patch", "networkpolicies", func(action ktesting.Action) (bool, runtime.Object, error) {
		pa := action.(interface{ GetPatch() []byte })
		var ops []map[string]any
		if err := json.Unmarshal(pa.GetPatch(), &ops); err != nil {
			t.Fatal(err)
		}
		if len(ops) != 3 || ops[0]["path"] != "/metadata/uid" || ops[1]["path"] != "/metadata/resourceVersion" || ops[2]["path"] != "/spec" {
			t.Fatalf("unsafe patch ops: %#v", ops)
		}
		return false, nil, nil
	})
	c, err := NewClient(client)
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.PatchSpec(context.Background(), "n", "p", "uid-1", "rv-2", networkingv1.NetworkPolicySpec{PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{"app": "db"}}}, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(cs.Actions()) != 1 {
		t.Fatalf("unexpected request count: %d", len(cs.Actions()))
	}
}

func TestPatchSpecRejectsMissingPreconditions(t *testing.T) {
	c, _ := NewClient(testTypedClient())
	if _, err := c.PatchSpec(context.Background(), "n", "p", "", "rv", networkingv1.NetworkPolicySpec{}, false); err == nil {
		t.Fatal("missing UID accepted")
	}
}
