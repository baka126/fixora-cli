package servicefix

import (
	"context"
	"encoding/json"
	jsonpatch "gopkg.in/evanphx/json-patch.v4"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	"testing"
)

type deliveryFixture struct {
	*runtimeFixture
	patches [][]byte
}

func (r *deliveryFixture) GetServiceDocument(context.Context, string, string) ([]byte, error) {
	return json.Marshal(r.service)
}
func (r *deliveryFixture) PatchService(_ context.Context, _, _ string, raw []byte) (*corev1.Service, error) {
	r.patches = append(r.patches, raw)
	p, err := jsonpatch.DecodePatch(raw)
	if err != nil {
		return nil, err
	}
	base, _ := json.Marshal(r.service)
	changed, err := p.Apply(base)
	if err != nil {
		return nil, err
	}
	var svc corev1.Service
	if err = json.Unmarshal(changed, &svc); err != nil {
		return nil, err
	}
	svc.ResourceVersion = "2"
	r.service = &svc
	return svc.DeepCopy(), nil
}
func deliverySetup(t *testing.T) (*deliveryFixture, Plan, *Receipt) {
	t.Helper()
	a, reader := serviceFixture(t)
	r := &deliveryFixture{runtimeFixture: &runtimeFixture{fixtureReader: reader}}
	p, err := Prepare(context.Background(), r, a, serviceFinding(), "")
	if err != nil {
		t.Fatal(err)
	}
	v, err := Verify(context.Background(), r, p)
	if err != nil {
		t.Fatal(err)
	}
	return r, p, v.Receipt
}
func TestServiceApplyRequiresFreshReceipt(t *testing.T) {
	for _, bad := range []string{"missing", "stale", "managed", "preview"} {
		t.Run(bad, func(t *testing.T) {
			r, p, receipt := deliverySetup(t)
			switch bad {
			case "missing":
				receipt = nil
			case "stale":
				r.service.ResourceVersion = "99"
			case "managed":
				p.Proof.sourceOnly = true
			case "preview":
				p.Diff = "changed"
			}
			_, err := Apply(context.Background(), r, p, receipt)
			if err == nil || len(r.patches) != 0 {
				t.Fatal("unsafe delivery")
			}
		})
	}
	r, p, receipt := deliverySetup(t)
	a, err := Apply(context.Background(), r, p, receipt)
	if err != nil || a == nil {
		t.Fatal(a, err)
	}
	var ops []map[string]any
	json.Unmarshal(r.patches[0], &ops)
	if ops[0]["path"] != "/metadata/uid" || ops[1]["path"] != "/metadata/resourceVersion" {
		t.Fatal(ops)
	}
	if r.service.Spec.ClusterIP != "10.0.0.1" || r.service.Spec.Selector["workload"] != "arbitrary" {
		t.Fatal(r.service)
	}
}
func TestServiceRollbackFieldConflicts(t *testing.T) {
	for _, bad := range []string{"none", "uid", "edited"} {
		t.Run(bad, func(t *testing.T) {
			r, p, receipt := deliverySetup(t)
			a, err := Apply(context.Background(), r, p, receipt)
			if err != nil {
				t.Fatal(err)
			}
			r.service.Labels = map[string]string{"concurrent": "preserve"}
			switch bad {
			case "uid":
				r.service.UID = "replacement"
			case "edited":
				r.service.Spec.Selector = map[string]string{"other": "value"}
			}
			out, err := Restore(context.Background(), r, a)
			if bad != "none" {
				if err == nil {
					t.Fatal("conflict ignored")
				}
				return
			}
			if err != nil || out.Status != "rollback-restored" || r.service.Labels["concurrent"] != "preserve" || r.service.Spec.Selector["workload"] != "wrong" {
				t.Fatal(out, err, r.service)
			}
		})
	}
}
func TestServiceLiveGateAndPartialOutcomes(t *testing.T) {
	r, p, receipt := deliverySetup(t)
	a, err := Apply(context.Background(), r, p, receipt)
	if err != nil {
		t.Fatal(err)
	}
	r.failure = "report"
	out, err := VerifyLive(context.Background(), r, p, a)
	if err == nil || !out.Changed || a == nil {
		t.Fatal(out, err)
	}
}

func (r *deliveryFixture) ListSlices(ctx context.Context, ns, name string) ([]discoveryv1.EndpointSlice, error) {
	if name == r.service.Name && r.service.Spec.Selector["workload"] == "arbitrary" {
		shadow := *r.runtimeFixture
		shadow.svc = r.service
		return shadow.ListSlices(ctx, ns, name)
	}
	return r.runtimeFixture.ListSlices(ctx, ns, name)
}

func TestServiceLiveGateSuccessUsesProductionOnly(t *testing.T) {
	r, p, receipt := deliverySetup(t)
	temporary := r.svc.UID
	a, err := Apply(context.Background(), r, p, receipt)
	if err != nil {
		t.Fatal(err)
	}
	out, err := VerifyLive(context.Background(), r, p, a)
	if err != nil || out.Status != "production-contract-passed" {
		t.Fatal(out, err)
	}
	if r.svc.UID != temporary || len(out.Verification.Cleanup) != 2 {
		t.Fatal("production gate created a replacement target Service")
	}
}
func TestServiceRollbackPreservesPortReorderAndRawPresence(t *testing.T) {
	r, p, receipt := deliverySetup(t)
	a, err := Apply(context.Background(), r, p, receipt)
	if err != nil {
		t.Fatal(err)
	}
	r.service.Spec.Ports = append([]corev1.ServicePort{{Name: "metrics", Port: 9091}}, r.service.Spec.Ports...)
	out, err := Restore(context.Background(), r, a)
	if err != nil || out.Status != "rollback-restored" || r.service.Spec.Ports[0].Name != "metrics" || r.service.Spec.Ports[1].TargetPort.IntVal != 9090 {
		t.Fatal(out, err, r.service.Spec.Ports)
	}
	for _, raw := range []string{`{"spec":{"ports":[{}]}}`, `{"spec":{"selector":null,"ports":[{"targetPort":null}]}}`} {
		s, target, err := captureFields([]byte(raw), 0)
		if err != nil {
			t.Fatal(err)
		}
		if s.Present != target.Present {
			t.Fatal(s, target)
		}
		if s.Present && string(s.Value) != "null" {
			t.Fatal(s)
		}
	}
}
