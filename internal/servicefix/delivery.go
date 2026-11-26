package servicefix

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"

	corev1 "k8s.io/api/core/v1"
)

type Outcome struct {
	Status       string        `json:"status"`
	Changed      bool          `json:"changed"`
	Summary      string        `json:"summary,omitempty"`
	Verification *Verification `json:"verification,omitempty"`
}
type Applied struct {
	proof                          *Proof
	service                        *corev1.Service
	selector, target               fieldValue
	selectorChanged, targetChanged bool
	binding                        string
}
type fieldValue struct {
	Present bool
	Value   json.RawMessage
}
type patchOp struct {
	Op    string `json:"op"`
	Path  string `json:"path"`
	Value any    `json:"value,omitempty"`
}
type serviceDocumentReader interface {
	GetServiceDocument(context.Context, string, string) ([]byte, error)
}

func (a *Applied) contentBinding() string {
	if a == nil {
		return ""
	}
	return digest([]any{a.proof.binding, a.service, a.selector, a.target, a.selectorChanged, a.targetChanged})
}
func (a *Applied) check() error {
	if a == nil || a.proof == nil || a.service == nil || a.binding == "" || a.binding != a.contentBinding() || a.proof.binding != a.proof.contentBinding() {
		return errors.New("private matching applied Service receipt required")
	}
	return nil
}
func Apply(ctx context.Context, r Runtime, plan Plan, receipt *Receipt) (*Applied, error) {
	ctx, cancel := operationContext(ctx)
	defer cancel()
	p := plan.Proof
	if r == nil || p == nil {
		return nil, errors.New("Service runtime and proof required")
	}
	if err := p.checkPlan(plan); err != nil {
		return nil, err
	}
	if err := receipt.Check(p); err != nil {
		return nil, err
	}
	if p.sourceOnly {
		return nil, errors.New("Helm/GitOps/operator Service requires reviewed source delivery")
	}
	if err := p.CheckFresh(ctx, r); err != nil {
		return nil, err
	}
	docReader, ok := r.(serviceDocumentReader)
	if !ok {
		return nil, errors.New("raw Service field-presence evidence required")
	}
	raw, err := docReader.GetServiceDocument(ctx, p.candidate.Namespace, p.candidate.Name)
	if err != nil {
		return nil, err
	}
	var stored corev1.Service
	if json.Unmarshal(raw, &stored) != nil {
		return nil, errors.New("invalid raw Service document")
	}
	stored.TypeMeta = p.before.Service.TypeMeta
	if !reflect.DeepEqual(&stored, p.before.Service) {
		return nil, errors.New("Service document changed before apply")
	}
	selector, target, err := captureFields(raw, p.portIndex)
	if err != nil {
		return nil, err
	}
	a := &Applied{proof: p, service: p.candidate.DeepCopy(), selector: selector, target: target, selectorChanged: !reflect.DeepEqual(p.before.Service.Spec.Selector, p.candidate.Spec.Selector), targetChanged: p.before.Service.Spec.Ports[p.portIndex].TargetPort != p.candidate.Spec.Ports[p.portIndex].TargetPort}
	ops := identityTests(p.before.Service)
	if a.selectorChanged {
		ops = append(ops, patchOp{"add", "/spec/selector", p.candidate.Spec.Selector})
	}
	if a.targetChanged {
		ops = append(ops, patchOp{"add", fmt.Sprintf("/spec/ports/%d/targetPort", p.portIndex), p.candidate.Spec.Ports[p.portIndex].TargetPort})
	}
	patch, _ := json.Marshal(ops)
	got, err := r.PatchService(ctx, p.candidate.Namespace, p.candidate.Name, patch)
	// Retain the intended change receipt even when transport fails after sending the patch.
	// A caller must inspect live state; retrying or rolling back is never automatic.
	if got != nil {
		a.service = got.DeepCopy()
	}
	a.binding = a.contentBinding()
	if err != nil {
		return a, errors.New("Service patch outcome is uncertain; inspect live routing before further action")
	}
	if !sameServiceIntent(p.candidate, got) {
		return a, errors.New("Service changed but returned routing or metadata differ; inspect live state")
	}
	return a, nil
}
func identityTests(s *corev1.Service) []patchOp {
	return []patchOp{{"test", "/metadata/uid", string(s.UID)}, {"test", "/metadata/resourceVersion", s.ResourceVersion}}
}
func captureFields(raw []byte, index int) (fieldValue, fieldValue, error) {
	var doc struct {
		Spec map[string]json.RawMessage `json:"spec"`
	}
	if json.Unmarshal(raw, &doc) != nil {
		return fieldValue{}, fieldValue{}, errors.New("invalid Service document")
	}
	selector, ok := doc.Spec["selector"]
	s := fieldValue{ok, selector}
	var ports []map[string]json.RawMessage
	if json.Unmarshal(doc.Spec["ports"], &ports) != nil || index < 0 || index >= len(ports) {
		return s, fieldValue{}, errors.New("selected port document unavailable")
	}
	target, ok := ports[index]["targetPort"]
	return s, fieldValue{ok, target}, nil
}
func (a *Applied) checkLive(ctx context.Context, r Reader) error {
	if err := a.check(); err != nil {
		return err
	}
	current, err := Collect(ctx, r, a.proof.authority)
	if err != nil {
		return err
	}
	if current.Service.UID != a.proof.candidate.UID || !reflect.DeepEqual(current.Service.Spec, a.proof.candidate.Spec) {
		return errors.New("production Service routing changed after apply")
	}
	// EndpointSlice convergence and resourceVersion legitimately change after patch.
	// All namespace, backend, Pod membership and source metadata remain bound.
	current.Service = current.Service.DeepCopy()
	current.Service.Spec = a.proof.before.Service.Spec
	current.Service.ResourceVersion = a.proof.before.Service.ResourceVersion
	current.Slices = a.proof.before.Slices
	if snapshotBinding(current) != snapshotBinding(a.proof.before) {
		return errors.New("production namespace, ownership or backend membership changed")
	}
	return nil
}
func VerifyLive(ctx context.Context, r Runtime, plan Plan, a *Applied) (Outcome, error) {
	ctx, cancel := operationContext(ctx)
	defer cancel()
	out := Outcome{Status: "production-verification-failed", Changed: true}
	if r == nil || a == nil || a.proof != plan.Proof {
		return out, errors.New("matching applied Service receipt required")
	}
	if err := a.proof.checkPlan(plan); err != nil {
		return out, err
	}
	if err := a.checkLive(ctx, r); err != nil {
		return out, err
	}
	svc, err := r.GetService(ctx, a.service.Namespace, a.service.Name)
	if err != nil {
		return out, err
	}
	v, err := verifySession(ctx, r, plan, svc, func(c context.Context) error { return a.checkLive(c, r) })
	out.Verification = &v
	if err != nil {
		return out, err
	}
	out.Status = "production-contract-passed"
	out.Summary = "Production Service and every approved backend passed the selected connectivity contract; consumer recovery is not established"
	return out, nil
}
