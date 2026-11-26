package servicefix

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
)

// Restore requires separate caller consent. It does not imply the old routing was healthy.
func Restore(ctx context.Context, r Runtime, a *Applied) (Outcome, error) {
	ctx, cancel := operationContext(ctx)
	defer cancel()
	out := Outcome{Status: "rollback-refused", Changed: true}
	if r == nil {
		return out, errors.New("Service runtime required")
	}
	if err := a.check(); err != nil {
		return out, err
	}
	s, err := r.GetService(ctx, a.proof.candidate.Namespace, a.proof.candidate.Name)
	if err != nil {
		return out, err
	}
	if s == nil || s.UID != a.proof.candidate.UID || s.DeletionTimestamp != nil {
		return out, errors.New("Service replaced or deleting; rollback refused")
	}
	index := -1
	want := a.proof.candidate.Spec.Ports[a.proof.portIndex]
	for i, p := range s.Spec.Ports {
		if p.Name == want.Name && p.Port == want.Port && p.Protocol == want.Protocol {
			if index >= 0 {
				return out, errors.New("selected port ambiguous")
			}
			index = i
		}
	}
	if index < 0 {
		return out, errors.New("selected Service port disappeared")
	}
	if a.selectorChanged && !reflect.DeepEqual(s.Spec.Selector, a.proof.candidate.Spec.Selector) || a.targetChanged && s.Spec.Ports[index].TargetPort != want.TargetPort {
		return out, errors.New("edited routing fields changed concurrently; rollback refused")
	}
	ops := identityTests(s)
	inverse := func(path string, v fieldValue) {
		if v.Present {
			ops = append(ops, patchOp{"add", path, v.Value})
		} else {
			ops = append(ops, patchOp{Op: "remove", Path: path})
		}
	}
	if a.selectorChanged {
		ops = append(ops, patchOp{"test", "/spec/selector", a.proof.candidate.Spec.Selector})
		inverse("/spec/selector", a.selector)
	}
	if a.targetChanged {
		path := fmt.Sprintf("/spec/ports/%d/targetPort", index)
		ops = append(ops, patchOp{"test", path, want.TargetPort})
		inverse(path, a.target)
	}
	patch, _ := json.Marshal(ops)
	got, err := r.PatchService(ctx, s.Namespace, s.Name, patch)
	if err != nil {
		return out, errors.New("rollback patch outcome uncertain; inspect live routing")
	}
	if got == nil || got.UID != s.UID {
		return out, errors.New("rollback response identity unavailable")
	}
	before := a.proof.before.Service
	expected := s.DeepCopy()
	if a.selectorChanged {
		expected.Spec.Selector = before.Spec.Selector
	}
	if a.targetChanged {
		expected.Spec.Ports[index].TargetPort = before.Spec.Ports[a.proof.portIndex].TargetPort
	}
	if !sameServiceIntent(expected, got) {
		out.Status = "rollback-uncertain"
		return out, errors.New("rollback response differs from the intended field-only inverse; inspect live routing and metadata")
	}

	out.Status = "rollback-restored"
	out.Summary = "Only Fixora-edited routing fields were restored; the original configuration may still be broken"
	return out, nil
}
