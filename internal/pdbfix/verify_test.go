package pdbfix

import (
	"context"
	"encoding/json"
	"errors"
	jsonpatch "github.com/evanphx/json-patch/v5"
	"github.com/fixora/kubectl-fixora/internal/kube"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	kfake "k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
	"testing"
	"time"
)

type runtimeFixture struct {
	*candidateReader
	dry, writes int
	rewrite     bool
	onDry       func()
	lose        bool
	writeErr    error
	block       bool
}

func (r *runtimeFixture) PatchPDB(ctx context.Context, ns, name string, b []byte, dry bool) (*policyv1.PodDisruptionBudget, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if ns != "team" || name != "protect" {
		return nil, errors.New("wrong target")
	}
	if dry {
		r.dry++
	} else {
		r.writes++
	}
	if r.writeErr != nil {
		return nil, r.writeErr
	}
	patch, e := jsonpatch.DecodePatch(b)
	if e != nil {
		return nil, e
	}
	raw, _ := json.Marshal(r.pdb)
	raw, e = patch.Apply(raw)
	if e != nil {
		return nil, e
	}
	var out policyv1.PodDisruptionBudget
	if e = json.Unmarshal(raw, &out); e != nil {
		return nil, e
	}
	if r.rewrite {
		out.Annotations = map[string]string{"unexpected": "mutation"}
	}
	if dry {
		if r.onDry != nil {
			r.onDry()
		}
		return &out, nil
	}
	out.Generation++
	out.ResourceVersion = "next-" + out.ResourceVersion
	out.Status.ObservedGeneration = out.Generation
	effect, e := EvaluateBudget(out.Spec, 3, 3)
	if e == nil {
		out.Status.DesiredHealthy = effect.RequiredHealthy
		out.Status.DisruptionsAllowed = effect.AllowedDisruptions
		out.Status.CurrentHealthy = 3
		out.Status.ExpectedPods = 3
		conditionStatus := metav1.ConditionFalse
		reason := "InsufficientPods"
		if effect.AllowedDisruptions > 0 {
			conditionStatus = metav1.ConditionTrue
			reason = "SufficientPods"
		}
		out.Status.Conditions = []metav1.Condition{{Type: "DisruptionAllowed", Status: conditionStatus, Reason: reason, ObservedGeneration: out.Generation, LastTransitionTime: metav1.Now()}}
	}
	r.pdb = out.DeepCopy()
	r.budgets = []policyv1.PodDisruptionBudget{*out.DeepCopy()}
	if r.lose {
		return nil, errors.New("lost write response")
	}
	return &out, nil
}
func (r *runtimeFixture) GetPDB(ctx context.Context, ns, name string) (*policyv1.PodDisruptionBudget, error) {
	if r.block {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return r.evidenceReader.GetPDB(ctx, ns, name)
}
func TestPDBVerifyDryRunOnly(t *testing.T) {
	a, cr := candidateFixture(t)
	r := &runtimeFixture{candidateReader: cr}
	p, e := Prepare(context.Background(), r, a, "")
	if e != nil {
		t.Fatal(e)
	}
	receipt, e := Verify(context.Background(), r, p)
	if e != nil || receipt == nil || r.dry != 1 || r.writes != 0 {
		t.Fatalf("invalid preflight: %v %d/%d", e, r.dry, r.writes)
	}
	r.rewrite = true
	if _, e = Verify(context.Background(), r, p); e == nil {
		t.Fatal("admission metadata rewrite accepted")
	}
	client := kfake.NewSimpleClientset(cr.pdb.DeepCopy())
	typed, e := NewClient(&kube.TypedClient{Clientset: client})
	if e != nil {
		t.Fatal(e)
	}
	client.PrependReactor("patch", "poddisruptionbudgets", func(action ktesting.Action) (bool, runtime.Object, error) {
		pa := action.(ktesting.PatchAction)
		var ops []map[string]any
		if e := json.Unmarshal(pa.GetPatch(), &ops); e != nil {
			t.Fatal(e)
		}
		if len(ops) < 3 || ops[0]["path"] != "/metadata/uid" || ops[1]["path"] != "/metadata/resourceVersion" {
			t.Fatal("unguarded patch", ops)
		}
		if action.(ktesting.PatchActionImpl).GetPatchOptions().DryRun[0] != "All" {
			t.Fatal("missing dry-run")
		}
		return true, cr.pdb.DeepCopy(), nil
	})
	if _, e = typed.PatchPDB(context.Background(), "team", "protect", []byte(`[{"op":"test","path":"/metadata/uid","value":"budget-id"},{"op":"test","path":"/metadata/resourceVersion","value":"7"},{"op":"add","path":"/spec/minAvailable","value":2}]`), true); e != nil {
		t.Fatal(e)
	}
}

func TestPDBVerifyFreshnessAndDeadline(t *testing.T) {
	a, cr := candidateFixture(t)
	r := &runtimeFixture{candidateReader: cr}
	p, e := Prepare(context.Background(), r, a, "")
	if e != nil {
		t.Fatal(e)
	}
	r.onDry = func() { r.pods[0].Status.Conditions = nil }
	if _, e = Verify(context.Background(), r, p); e == nil {
		t.Fatal("readiness drift accepted")
	}
	r.onDry = nil
	r.block = true
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, e = Verify(ctx, r, p); !errors.Is(e, context.DeadlineExceeded) {
		t.Fatalf("lost deadline cause: %v", e)
	}
	if time.Since(start) > time.Second {
		t.Fatal("initial read exceeded parent deadline")
	}
	ctx2, cancel2 := operationContext(context.Background())
	defer cancel2()
	deadline, ok := ctx2.Deadline()
	if !ok || time.Until(deadline) > 3*time.Minute || time.Until(deadline) < 2*time.Minute {
		t.Fatal("missing standalone bound")
	}
}
func TestPDBReceiptCannotReplayOrSerialize(t *testing.T) {
	a, cr := candidateFixture(t)
	r := &runtimeFixture{candidateReader: cr}
	p, e := Prepare(context.Background(), r, a, "")
	if e != nil {
		t.Fatal(e)
	}
	rec, e := Verify(context.Background(), r, p)
	if e != nil {
		t.Fatal(e)
	}
	b, _ := json.Marshal(rec)
	var copy Receipt
	json.Unmarshal(b, &copy)
	if e = copy.consume(p); e == nil {
		t.Fatal("serialized receipt accepted")
	}
	tampered := p
	tampered.Status = "healthy"
	if _, e = Verify(context.Background(), r, tampered); e == nil {
		t.Fatal("tampered plan accepted")
	}
	if e = rec.consume(p); e != nil {
		t.Fatal(e)
	}
	if e = rec.consume(p); e == nil {
		t.Fatal("receipt replay accepted")
	}
}
