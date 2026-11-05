package networkpolicyfix

import (
	"context"
	"errors"
	"reflect"
	"sync"

	networkingv1 "k8s.io/api/networking/v1"
)

type specWriter interface {
	Runtime
	PatchSpec(context.Context, string, string, string, string, networkingv1.NetworkPolicySpec, bool) (*networkingv1.NetworkPolicy, error)
}

type Applied struct {
	Target                 Target                         `json:"target"`
	Status                 string                         `json:"status"`
	PolicyUID              string                         `json:"policyUID"`
	AppliedResourceVersion string                         `json:"appliedResourceVersion,omitempty"`
	AppliedSpec            networkingv1.NetworkPolicySpec `json:"appliedSpec"`
	OriginalSpec           networkingv1.NetworkPolicySpec `json:"originalSpec"`
	private                *appliedProof
}
type appliedProof struct {
	mu        sync.Mutex
	authority *Authority
	plan      string
	uid       string
	applied   networkingv1.NetworkPolicySpec
	original  networkingv1.NetworkPolicySpec
	rv        string
	restored  bool
}

type Outcome struct {
	Status string `json:"status"`
	Target Target `json:"target"`
	Detail string `json:"detail,omitempty"`
}

func Apply(ctx context.Context, runtime specWriter, plan Plan, receipt *Receipt) (*Applied, error) {
	if runtime == nil || plan.CheckBound() != nil {
		return nil, errors.New("fresh NetworkPolicy plan and typed runtime required")
	}
	if receipt == nil {
		return nil, errors.New("successful isolated verification required before apply")
	}
	if err := receipt.Check(plan); err != nil {
		return nil, err
	}
	if err := plan.proof.authority.CheckUnchanged(); err != nil {
		return nil, err
	}
	fresh, err := Prepare(ctx, runtime, plan.proof.authority, plan.Target)
	if err != nil || fresh.proof.state != plan.proof.state {
		return nil, errors.New("policy, workload or Pod inventory changed after verification")
	}
	if err = receipt.consume(plan); err != nil {
		return nil, err
	}
	current, err := runtime.GetPolicy(ctx, plan.Target.Namespace, plan.Target.Name)
	if err != nil {
		return nil, errors.New("policy changed before apply")
	}
	if current.UID != plan.Candidate.UID || current.ResourceVersion != plan.Candidate.ResourceVersion || !reflect.DeepEqual(current.Spec, plan.OriginalSpec) {
		return nil, errors.New("policy identity or spec changed before apply")
	}
	result, err := runtime.PatchSpec(ctx, plan.Target.Namespace, plan.Target.Name, string(current.UID), current.ResourceVersion, plan.Candidate.Spec, false)
	if err != nil {
		observed, readErr := runtime.GetPolicy(context.WithoutCancel(ctx), plan.Target.Namespace, plan.Target.Name)
		if readErr == nil && observed.UID == current.UID && reflect.DeepEqual(observed.Spec, plan.Candidate.Spec) {
			if observed.ResourceVersion != "" {
				return newApplied(plan, observed, "apply-response-recovered"), nil
			}
			return nil, errors.New("apply response was lost; candidate spec is observed but identity version is incomplete")
		}
		return nil, errors.New("apply outcome is uncertain; re-read the exact policy before retrying")
	}
	if result == nil || result.UID != current.UID || result.ResourceVersion == "" || !reflect.DeepEqual(result.Spec, plan.Candidate.Spec) {
		return nil, errors.New("server did not confirm the exact NetworkPolicy replacement")
	}
	return newApplied(plan, result, "applied-policy-spec"), nil
}

func newApplied(plan Plan, result *networkingv1.NetworkPolicy, status string) *Applied {
	return &Applied{Target: plan.Target, Status: status, PolicyUID: string(result.UID), AppliedResourceVersion: result.ResourceVersion, AppliedSpec: *result.Spec.DeepCopy(), OriginalSpec: *plan.OriginalSpec.DeepCopy(), private: &appliedProof{authority: plan.proof.authority, plan: plan.proof.public, uid: string(result.UID), applied: *result.Spec.DeepCopy(), original: *plan.OriginalSpec.DeepCopy(), rv: result.ResourceVersion}}
}

func Observe(ctx context.Context, runtime Runtime, applied *Applied) (Outcome, error) {
	if runtime == nil || applied == nil || applied.private == nil {
		return Outcome{}, errors.New("application receipt required")
	}
	current, err := runtime.GetPolicy(ctx, applied.Target.Namespace, applied.Target.Name)
	if err != nil {
		return Outcome{Status: "observation-unavailable", Target: applied.Target}, nil
	}
	if string(current.UID) != applied.private.uid {
		return Outcome{Status: "policy-replaced", Target: applied.Target}, nil
	}
	if reflect.DeepEqual(current.Spec, applied.private.applied) {
		return Outcome{Status: "policy-spec-observed", Target: applied.Target}, nil
	}
	return Outcome{Status: "policy-spec-drifted", Target: applied.Target, Detail: "The admitted policy spec no longer matches the applied spec."}, nil
}

func Restore(ctx context.Context, runtime specWriter, applied *Applied, consent bool) (Outcome, error) {
	if !consent {
		return Outcome{}, errors.New("explicit rollback consent required")
	}
	if runtime == nil || applied == nil || applied.private == nil {
		return Outcome{}, errors.New("application receipt required")
	}
	p := applied.private
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.restored {
		return Outcome{}, errors.New("NetworkPolicy rollback already consumed")
	}
	current, err := runtime.GetPolicy(ctx, applied.Target.Namespace, applied.Target.Name)
	if err != nil {
		return Outcome{Status: "rollback-outcome-uncertain", Target: applied.Target}, nil
	}
	if string(current.UID) != p.uid {
		return Outcome{Status: "rollback-refused-drift", Target: applied.Target, Detail: "Policy identity or spec changed after apply; original spec was not restored."}, nil
	}
	if reflect.DeepEqual(current.Spec, p.original) {
		p.restored = true
		return Outcome{Status: "original-policy-spec-restored", Target: applied.Target}, nil
	}
	if !reflect.DeepEqual(current.Spec, p.applied) {
		return Outcome{Status: "rollback-refused-drift", Target: applied.Target, Detail: "Policy identity or spec changed after apply; original spec was not restored."}, nil
	}
	result, err := runtime.PatchSpec(ctx, applied.Target.Namespace, applied.Target.Name, p.uid, current.ResourceVersion, p.original, false)
	if err != nil {
		observed, readErr := runtime.GetPolicy(context.WithoutCancel(ctx), applied.Target.Namespace, applied.Target.Name)
		if readErr != nil || observed == nil || string(observed.UID) != p.uid {
			return Outcome{Status: "rollback-outcome-uncertain", Target: applied.Target}, nil
		}
		if reflect.DeepEqual(observed.Spec, p.original) {
			p.restored = true
			return Outcome{Status: "original-policy-spec-restored", Target: applied.Target}, nil
		}
		if reflect.DeepEqual(observed.Spec, p.applied) {
			return Outcome{Status: "rollback-not-applied", Target: applied.Target, Detail: "The original policy spec remains unchanged; rollback may be explicitly retried."}, nil
		}
		return Outcome{Status: "rollback-refused-drift", Target: applied.Target, Detail: "Policy spec changed during rollback; no retry was attempted."}, nil
	}
	if result == nil || string(result.UID) != p.uid || !reflect.DeepEqual(result.Spec, p.original) {
		observed, readErr := runtime.GetPolicy(context.WithoutCancel(ctx), applied.Target.Namespace, applied.Target.Name)
		if readErr == nil && observed != nil && string(observed.UID) == p.uid && reflect.DeepEqual(observed.Spec, p.original) {
			p.restored = true
			return Outcome{Status: "original-policy-spec-restored", Target: applied.Target}, nil
		}
		return Outcome{Status: "rollback-outcome-uncertain", Target: applied.Target}, nil
	}
	p.restored = true
	return Outcome{Status: "original-policy-spec-restored", Target: applied.Target}, nil
}
