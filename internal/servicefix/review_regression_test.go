package servicefix

import (
	"context"
	"encoding/json"
	"errors"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/util/strategicpatch"
	syaml "sigs.k8s.io/yaml"
	"strings"
	"testing"
)

func TestServiceReviewPatchUsesKubernetesScalarTargetPort(t *testing.T) {
	a, r := serviceFixture(t)
	p, err := Prepare(context.Background(), r, a, serviceFinding(), "")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := p.PatchYAML()
	if err != nil {
		t.Fatal(err)
	}
	var svc corev1.Service
	if err = syaml.Unmarshal([]byte(raw), &svc); err != nil {
		t.Fatalf("saved Service patch is not valid Kubernetes YAML: %v\n%s", err, raw)
	}
}

type rewrittenRollback struct{ *deliveryFixture }

func (r rewrittenRollback) PatchService(ctx context.Context, ns, name string, b []byte) (*corev1.Service, error) {
	got, err := r.deliveryFixture.PatchService(ctx, ns, name, b)
	if err == nil {
		got.Spec.Selector = map[string]string{"webhook": "different"}
		r.service = got.DeepCopy()
	}
	return got, err
}
func TestServiceReviewRollbackChecksRestoredFields(t *testing.T) {
	r, p, receipt := deliverySetup(t)
	a, err := Apply(context.Background(), r, p, receipt)
	if err != nil {
		t.Fatal(err)
	}
	out, err := Restore(context.Background(), rewrittenRollback{r}, a)
	if err == nil {
		t.Fatalf("claimed restoration after admission rewrote selector: %#v, selector=%v", out, r.service.Spec.Selector)
	}
}

type deadlineReader struct {
	*runtimeFixture
	sawDeadline bool
}

func (r *deadlineReader) GetNamespace(ctx context.Context, ns string) (*corev1.Namespace, error) {
	_, r.sawDeadline = ctx.Deadline()
	return nil, errors.New("stop before I/O")
}
func TestServiceReviewDefaultDeadlineCoversFirstRead(t *testing.T) {
	a, reader := serviceFixture(t)
	base := &runtimeFixture{fixtureReader: reader}
	p, err := Prepare(context.Background(), base, a, serviceFinding(), "")
	if err != nil {
		t.Fatal(err)
	}
	r := &deadlineReader{runtimeFixture: base}
	Verify(context.Background(), r, p)
	if !r.sawDeadline {
		t.Fatal("Verify's first API call has no deadline")
	}
}

type failedCreate struct{ *runtimeFixture }

func (r failedCreate) CreateService(ctx context.Context, s *corev1.Service) (*corev1.Service, error) {
	r.runtimeFixture.CreateService(ctx, s)
	return nil, errors.New("server persisted create but response was lost")
}
func TestServiceReviewUnknownCreateIsRecoveredOrReported(t *testing.T) {
	a, reader := serviceFixture(t)
	base := &runtimeFixture{fixtureReader: reader}
	r := failedCreate{base}
	p, err := Prepare(context.Background(), r, a, serviceFinding(), "")
	if err != nil {
		t.Fatal(err)
	}
	v, err := Verify(context.Background(), r, p)
	if err == nil {
		t.Fatal("expected create error")
	}
	if len(r.deleted) == 0 && len(v.Cleanup) == 0 {
		t.Fatalf("created Service %s remains with neither recovery nor cleanup entry: %#v, %v", r.svc.Name, v, err)
	}
}

type swappedLogPod struct {
	*runtimeFixture
	swapped        bool
	readsAfterSwap int
}

func (r *swappedLogPod) PodLogs(ctx context.Context, ns, name string, limit int64) ([]byte, error) {
	// A replacement can copy the publicly readable request/nonce and return fabricated checks.
	b, err := r.runtimeFixture.PodLogs(ctx, ns, name, limit)
	r.pod.UID = "replacement"
	r.pod.Spec.Containers[0].Image = "untrusted/replacement:latest"
	r.swapped = true
	return b, err
}
func (r *swappedLogPod) ReadPod(ctx context.Context, ns, name string) (*corev1.Pod, error) {
	if r.swapped {
		r.readsAfterSwap++
	}
	return r.runtimeFixture.ReadPod(ctx, ns, name)
}
func (r *swappedLogPod) DeleteOwned(ctx context.Context, ref ObjectRef) error {
	// The replacement exits and is deleted before cleanup: real DeleteOwned treats NotFound as success.
	return r.runtimeFixture.DeleteOwned(ctx, ref)
}
func TestServiceReviewReportRemainsBoundToProbeUID(t *testing.T) {
	a, reader := serviceFixture(t)
	r := &swappedLogPod{runtimeFixture: &runtimeFixture{fixtureReader: reader}}
	p, err := Prepare(context.Background(), r, a, serviceFinding(), "")
	if err != nil {
		t.Fatal(err)
	}
	v, err := Verify(context.Background(), r, p)
	if err == nil && v.Receipt != nil {
		t.Fatalf("issued receipt for name-only logs from a replaced Pod; rechecks after logs=%d", r.readsAfterSwap)
	}
}

type ownerAdmission struct{ *runtimeFixture }

func (r ownerAdmission) Admit(ctx context.Context, s *corev1.Service) (*corev1.Service, error) {
	got, err := r.runtimeFixture.Admit(ctx, s)
	got.Finalizers = []string{"external.example/hold"}
	return got, err
}
func TestServiceReviewDryRunRejectsUnauthorizedMetadata(t *testing.T) {
	a, reader := serviceFixture(t)
	r := ownerAdmission{&runtimeFixture{fixtureReader: reader}}
	p, err := Prepare(context.Background(), r, a, serviceFinding(), "")
	if err != nil {
		t.Fatal(err)
	}
	v, err := Verify(context.Background(), r, p)
	if err == nil && v.Receipt != nil {
		t.Fatal("dry-run admitted a finalizer outside the approved routing fields and issued a receipt")
	}
}

func TestServiceReviewNumericAndNamedPatchScalars(t *testing.T) {
	for _, numeric := range []bool{false, true} {
		a, r := serviceFixture(t)
		if numeric {
			var err error
			a, err = LoadPolicy(policyFile(t, strings.Replace(validPolicy, "targetPort: http", "targetPort: 8080", 1)))
			if err != nil {
				t.Fatal(err)
			}
		}
		r.service.Spec.Selector["obsolete"] = "remove"
		p, err := Prepare(context.Background(), r, a, serviceFinding(), "")
		if err != nil {
			t.Fatal(err)
		}
		raw, err := p.PatchYAML()
		if err != nil {
			t.Fatal(err)
		}
		var got corev1.Service
		if err = syaml.Unmarshal([]byte(raw), &got); err != nil {
			t.Fatal(err)
		}
		if got.Spec.Ports[0].TargetPort != p.Proof.candidate.Spec.Ports[0].TargetPort {
			t.Fatal("scalar type changed")
		}
		var preview map[string]any
		if err = syaml.Unmarshal([]byte(p.Diff), &preview); err != nil {
			t.Fatal(err)
		}
		if _, bad := preview["targetPort"].(map[string]any); bad {
			t.Fatal("preview contains IntOrString internals")
		}
		base, _ := json.Marshal(r.service)
		patch, err := syaml.YAMLToJSON([]byte(raw))
		if err != nil {
			t.Fatal(err)
		}
		merged, err := strategicpatch.StrategicMergePatch(base, patch, corev1.Service{})
		if err != nil {
			t.Fatal(err)
		}
		got = corev1.Service{}
		if err = json.Unmarshal(merged, &got); err != nil {
			t.Fatal(err)
		}
		if len(got.Spec.Selector) != 1 || got.Spec.Selector["workload"] != "arbitrary" {
			t.Fatal("obsolete selector key retained", got.Spec.Selector)
		}
	}
}

type deliveryMetadataRewrite struct{ *deliveryFixture }

func (r deliveryMetadataRewrite) PatchService(ctx context.Context, ns, name string, raw []byte) (*corev1.Service, error) {
	got, err := r.deliveryFixture.PatchService(ctx, ns, name, raw)
	if got != nil {
		got.Finalizers = []string{"unapproved.example/hold"}
	}
	return got, err
}
func TestServiceReviewMetadataFreshnessAndProductionResponse(t *testing.T) {
	r, p, receipt := deliverySetup(t)
	r.service.Finalizers = []string{"other.example/hold"}
	if _, err := Apply(context.Background(), r, p, receipt); err == nil || len(r.patches) != 0 {
		t.Fatal("new finalizer escaped freshness")
	}
	r, p, receipt = deliverySetup(t)
	applied, err := Apply(context.Background(), deliveryMetadataRewrite{r}, p, receipt)
	if err == nil || applied == nil {
		t.Fatal("production metadata rewrite not reported as changed/uncertain")
	}
}

type lostCreateResponse struct {
	*runtimeFixture
	kind        string
	findFailure string
}

func (r lostCreateResponse) CreateService(ctx context.Context, s *corev1.Service) (*corev1.Service, error) {
	got, err := r.runtimeFixture.CreateService(ctx, s)
	if r.kind == "Service" {
		return nil, errors.New("lost response")
	}
	return got, err
}
func (r lostCreateResponse) CreatePolicy(ctx context.Context, n *networkingv1.NetworkPolicy) (*networkingv1.NetworkPolicy, error) {
	got, err := r.runtimeFixture.CreatePolicy(ctx, n)
	if r.kind == "NetworkPolicy" {
		return nil, errors.New("lost response")
	}
	return got, err
}
func (r lostCreateResponse) CreatePod(ctx context.Context, p *corev1.Pod) (*corev1.Pod, error) {
	got, err := r.runtimeFixture.CreatePod(ctx, p)
	if r.kind == "Pod" {
		return nil, errors.New("lost response")
	}
	return got, err
}
func (r lostCreateResponse) FindAttempt(ctx context.Context, kind, ns, nonce string) ([]ObjectRef, error) {
	if r.findFailure == "denied" {
		return nil, errors.New("denied")
	}
	if r.findFailure == "wrong-template" && r.svc != nil {
		r.svc.Spec.Selector = map[string]string{"foreign": "value"}
	}
	refs, err := r.runtimeFixture.FindAttempt(ctx, kind, ns, nonce)
	if r.findFailure == "ambiguous" {
		refs = append(refs, refs...)
	}
	return refs, err
}
func TestServiceReviewLostCreatesRecoverOnlyExactAttempt(t *testing.T) {
	for _, kind := range []string{"Service", "NetworkPolicy", "Pod"} {
		t.Run(kind, func(t *testing.T) {
			a, reader := serviceFixture(t)
			base := &runtimeFixture{fixtureReader: reader}
			r := lostCreateResponse{runtimeFixture: base, kind: kind}
			p, err := Prepare(context.Background(), r, a, serviceFinding(), "")
			if err != nil {
				t.Fatal(err)
			}
			v, err := Verify(context.Background(), r, p)
			if err == nil || v.Receipt != nil {
				t.Fatal("lost create certified")
			}
			found := false
			for _, ref := range base.deleted {
				if ref.Kind == kind {
					found = true
				}
			}
			if !found || v.Status == "cleanup-incomplete" {
				t.Fatal("exact persisted create was not recovered", v, base.deleted)
			}
		})
	}
	for _, bad := range []string{"denied", "ambiguous", "wrong-template"} {
		t.Run(bad, func(t *testing.T) {
			a, reader := serviceFixture(t)
			base := &runtimeFixture{fixtureReader: reader}
			r := lostCreateResponse{runtimeFixture: base, kind: "Service", findFailure: bad}
			p, err := Prepare(context.Background(), r, a, serviceFinding(), "")
			if err != nil {
				t.Fatal(err)
			}
			v, err := Verify(context.Background(), r, p)
			if err == nil || v.Receipt != nil || v.Status != "cleanup-incomplete" || len(v.Cleanup) == 0 || len(base.deleted) != 0 {
				t.Fatal("unproven object deleted or incomplete creation hidden", v, base.deleted)
			}
		})
	}
}
