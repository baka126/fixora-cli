package rbacfix

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
)

func Apply(ctx context.Context, r Runtime, a *Authority, approved *Plan, store *GrantStore) (*Outcome, error) {
	if store == nil {
		return nil, errors.New("private grant receipt store is required")
	}
	if approved == nil {
		return nil, errors.New("reviewed RBAC plan is required")
	}
	fresh, err := Prepare(ctx, r, a)
	if err != nil {
		return nil, err
	}
	if fresh.digest != approved.digest || fresh.Namespace != approved.Namespace || fresh.ServiceAccount != approved.ServiceAccount || !sameRole(fresh.Role, approved.Role) || !sameRoleBinding(fresh.RoleBinding, approved.RoleBinding) || !reflect.DeepEqual(fresh.Requests, approved.Requests) || fresh.serviceAccountUID != approved.serviceAccountUID || fresh.serviceAccountRV != approved.serviceAccountRV {
		return nil, errors.New("RBAC policy or live authorization state changed after preview; review a new preview")
	}
	if err = beforeRole(ctx, r, a, fresh); err != nil {
		return nil, fmt.Errorf("pre-create checks failed: %w", err)
	}
	role, err := r.CreateRole(ctx, fresh.Role)
	if err != nil {
		return &Outcome{Status: "apply-uncertain", Namespace: fresh.Namespace, Role: fresh.Role.Name, Message: "Role create outcome is uncertain; inspect the exact object before retrying."}, nil
	}
	if role == nil || role.UID == "" {
		return &Outcome{Status: "apply-uncertain", Namespace: fresh.Namespace, Role: fresh.Role.Name, Message: "Role create response could not be verified; no object was adopted or deleted."}, nil
	}
	roleUID := role.UID
	if !sameRole(fresh.Role, role) {
		return cleanupFailure(ctx, r, fresh, roleUID, "created Role did not match the approved candidate"), nil
	}
	observed, err := r.GetRole(ctx, fresh.Namespace, role.Name)
	if err != nil || observed.UID != roleUID || !sameRole(fresh.Role, observed) {
		return cleanupFailure(ctx, r, fresh, roleUID, "created Role read-back could not be verified"), nil
	}
	if err = beforeBinding(ctx, r, a, fresh, roleUID); err != nil {
		return cleanupFailure(ctx, r, fresh, roleUID, "pre-create checks changed: "+err.Error()), nil
	}
	binding, err := r.CreateRoleBinding(ctx, fresh.RoleBinding)
	if err != nil {
		return &Outcome{Status: "apply-uncertain", Namespace: fresh.Namespace, Role: fresh.Role.Name, RoleBinding: fresh.RoleBinding.Name, Message: "RoleBinding create outcome is uncertain; inspect both named objects before retrying."}, nil
	}
	if binding == nil || binding.UID == "" {
		return &Outcome{Status: "apply-uncertain", Namespace: fresh.Namespace, Role: fresh.Role.Name, RoleBinding: fresh.RoleBinding.Name, Message: "RoleBinding response could not be verified; inspect the objects before cleanup or retry."}, nil
	}
	bindingUID := binding.UID
	if !sameRoleBinding(fresh.RoleBinding, binding) {
		return cleanupFailureWithBinding(ctx, r, fresh, bindingUID, roleUID, "created RoleBinding did not match the approved candidate"), nil
	}
	observedBinding, err := r.GetRoleBinding(ctx, fresh.Namespace, binding.Name)
	if err != nil || observedBinding.UID != bindingUID || !sameRoleBinding(fresh.RoleBinding, observedBinding) {
		return cleanupFailureWithBinding(ctx, r, fresh, bindingUID, roleUID, "created RoleBinding read-back could not be verified"), nil
	}
	observedSA, err := r.GetServiceAccount(ctx, fresh.Namespace, fresh.ServiceAccount)
	if err != nil || observedSA.UID != fresh.serviceAccountUID || observedSA.ResourceVersion != fresh.serviceAccountRV || gitOpsManaged(observedSA.Labels, observedSA.Annotations) {
		return cleanupFailureWithBinding(ctx, r, fresh, bindingUID, roleUID, "ServiceAccount identity changed while the grant was being created"), nil
	}
	observedRole, err := r.GetRole(ctx, fresh.Namespace, role.Name)
	if err != nil || observedRole.UID != roleUID || !sameRole(fresh.Role, observedRole) {
		return cleanupFailureWithBinding(ctx, r, fresh, bindingUID, roleUID, "Role changed while the grant was being created"), nil
	}
	decisions, err := verifyAllowed(ctx, r, fresh)
	if err != nil {
		return cleanupFailureWithBinding(ctx, r, fresh, bindingUID, roleUID, "post-create authorization could not be confirmed"), nil
	}
	receipt, saveErr := store.Create(GrantReceipt{Namespace: fresh.Namespace, ServiceAccount: fresh.ServiceAccount, ServiceAccountUID: string(fresh.serviceAccountUID), PolicyDigest: a.Digest(), Requests: append([]Request(nil), fresh.Requests...), Role: role.DeepCopy(), RoleBinding: binding.DeepCopy()})
	if saveErr != nil {
		cleanup := cleanupFailureWithBinding(ctx, r, fresh, bindingUID, roleUID, "verified grant receipt could not be saved: "+saveErr.Error())
		cleanup.Status = "receipt-save-failed-" + cleanup.Status
		cleanup.Message += " Receipt persistence failed; revoke will not be available for this grant."
		return cleanup, nil
	}
	return &Outcome{Status: "applied-and-authorized", Namespace: fresh.Namespace, Role: fresh.Role.Name, RoleBinding: fresh.RoleBinding.Name, Decisions: decisions, GrantID: receipt.ID, ReceiptPath: store.path(receipt.ID), Message: "The requested ServiceAccount permissions are authorized. Workload recovery was not evaluated."}, nil
}

func beforeBinding(ctx context.Context, r Runtime, a *Authority, p *Plan, roleUID types.UID) error {
	if err := a.CheckFresh(); err != nil {
		return err
	}
	ns, err := r.GetNamespace(ctx, p.Namespace)
	if err != nil {
		return err
	}
	if gitOpsManaged(ns.Labels, ns.Annotations) {
		return errors.New("namespace ownership changed")
	}
	sa, err := r.GetServiceAccount(ctx, p.Namespace, p.ServiceAccount)
	if err != nil {
		return err
	}
	if sa.UID != p.serviceAccountUID || sa.ResourceVersion != p.serviceAccountRV || gitOpsManaged(sa.Labels, sa.Annotations) {
		return errors.New("ServiceAccount identity changed")
	}
	role, err := r.GetRole(ctx, p.Namespace, p.Role.Name)
	if err != nil {
		return err
	}
	if role.UID != roleUID || !sameRole(p.Role, role) {
		return errors.New("created Role identity changed")
	}
	for _, q := range []Request{{Namespace: p.Namespace, APIGroup: "rbac.authorization.k8s.io", Resource: "roles", Verb: "get"}, {Namespace: p.Namespace, APIGroup: "rbac.authorization.k8s.io", Resource: "roles", Verb: "delete"}, {Namespace: p.Namespace, APIGroup: "rbac.authorization.k8s.io", Resource: "rolebindings", Verb: "create"}, {Namespace: p.Namespace, APIGroup: "rbac.authorization.k8s.io", Resource: "rolebindings", Verb: "get"}, {Namespace: p.Namespace, APIGroup: "rbac.authorization.k8s.io", Resource: "rolebindings", Verb: "list"}, {Namespace: p.Namespace, APIGroup: "rbac.authorization.k8s.io", Resource: "rolebindings", Verb: "delete"}} {
		d, e := r.CheckSelfAccess(ctx, q)
		if e != nil || !d.Allowed || d.EvaluationError != "" {
			return fmt.Errorf("caller access check failed for %s rolebindings", q.Verb)
		}
	}
	for _, q := range p.Requests {
		d, e := r.CheckSubjectAccess(ctx, "system:serviceaccount:"+p.Namespace+":"+p.ServiceAccount, []string{"system:serviceaccounts", "system:serviceaccounts:" + p.Namespace, "system:authenticated"}, q)
		if e != nil || d.EvaluationError != "" || (!d.Denied && !d.Allowed) || d.Allowed {
			return fmt.Errorf("target authorization state changed for %s %s", q.Verb, q.Resource)
		}
	}
	if _, err = r.GetRoleBinding(ctx, p.Namespace, p.RoleBinding.Name); err == nil {
		return errors.New("RoleBinding name appeared after preview")
	}
	if !apierrors.IsNotFound(err) {
		return err
	}
	admitted, err := r.DryRunRoleBinding(ctx, p.RoleBinding)
	if err != nil {
		return err
	}
	if !sameRoleBinding(p.RoleBinding, admitted) {
		return errors.New("admission changed the RoleBinding")
	}
	return nil
}

func beforeRole(ctx context.Context, r Runtime, a *Authority, p *Plan) error {
	if err := a.CheckFresh(); err != nil {
		return err
	}
	ns, err := r.GetNamespace(ctx, p.Namespace)
	if err != nil {
		return err
	}
	if gitOpsManaged(ns.Labels, ns.Annotations) {
		return errors.New("namespace ownership changed")
	}
	sa, err := r.GetServiceAccount(ctx, p.Namespace, p.ServiceAccount)
	if err != nil {
		return err
	}
	if sa.UID != p.serviceAccountUID || sa.ResourceVersion != p.serviceAccountRV || gitOpsManaged(sa.Labels, sa.Annotations) {
		return errors.New("ServiceAccount identity changed")
	}
	if _, err = r.GetRole(ctx, p.Namespace, p.Role.Name); err == nil {
		return errors.New("Role name appeared after preview")
	}
	if !apierrors.IsNotFound(err) {
		return err
	}
	if _, err = r.GetRoleBinding(ctx, p.Namespace, p.RoleBinding.Name); err == nil {
		return errors.New("RoleBinding name appeared after preview")
	}
	if !apierrors.IsNotFound(err) {
		return err
	}
	for _, q := range []Request{{Namespace: p.Namespace, APIGroup: "rbac.authorization.k8s.io", Resource: "roles", Verb: "create"}, {Namespace: p.Namespace, APIGroup: "rbac.authorization.k8s.io", Resource: "roles", Verb: "get"}, {Namespace: p.Namespace, APIGroup: "rbac.authorization.k8s.io", Resource: "roles", Verb: "delete"}, {Namespace: p.Namespace, APIGroup: "rbac.authorization.k8s.io", Resource: "rolebindings", Verb: "create"}, {Namespace: p.Namespace, APIGroup: "rbac.authorization.k8s.io", Resource: "rolebindings", Verb: "get"}, {Namespace: p.Namespace, APIGroup: "rbac.authorization.k8s.io", Resource: "rolebindings", Verb: "list"}, {Namespace: p.Namespace, APIGroup: "rbac.authorization.k8s.io", Resource: "rolebindings", Verb: "delete"}} {
		d, e := r.CheckSelfAccess(ctx, q)
		if e != nil || d.EvaluationError != "" || !d.Allowed {
			return fmt.Errorf("caller is not confirmed authorized to %s %s", q.Verb, q.Resource)
		}
	}
	for _, q := range p.Requests {
		d, e := r.CheckSubjectAccess(ctx, "system:serviceaccount:"+p.Namespace+":"+p.ServiceAccount, []string{"system:serviceaccounts", "system:serviceaccounts:" + p.Namespace, "system:authenticated"}, q)
		if e != nil || d.EvaluationError != "" || (!d.Denied && !d.Allowed) || d.Allowed {
			return fmt.Errorf("target authorization state changed for %s %s", q.Verb, q.Resource)
		}
	}
	return nil
}

func verifyAllowed(ctx context.Context, r Runtime, p *Plan) ([]Decision, error) {
	out := make([]Decision, 0, len(p.Requests))
	for _, q := range p.Requests {
		d, e := r.CheckSubjectAccess(ctx, "system:serviceaccount:"+p.Namespace+":"+p.ServiceAccount, []string{"system:serviceaccounts", "system:serviceaccounts:" + p.Namespace, "system:authenticated"}, q)
		if e != nil || d.EvaluationError != "" || !d.Allowed {
			return nil, fmt.Errorf("authorization not confirmed for %s %s", q.Verb, q.Resource)
		}
		out = append(out, d)
	}
	return out, nil
}

func cleanupFailure(ctx context.Context, r Runtime, p *Plan, roleUID types.UID, msg string) *Outcome {
	return cleanupFailureWithBinding(ctx, r, p, "", roleUID, msg)
}
func cleanupFailureWithBinding(ctx context.Context, r Runtime, p *Plan, bindingUID, roleUID types.UID, msg string) *Outcome {
	cctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	status := "authorization-failed"
	bindingName := ""
	if bindingUID != "" {
		bindingName = p.RoleBinding.Name
		if e := r.DeleteRoleBinding(cctx, p.Namespace, p.RoleBinding.Name, bindingUID); e != nil {
			status = "cleanup-incomplete"
			return &Outcome{Status: status, Namespace: p.Namespace, Role: p.Role.Name, RoleBinding: p.RoleBinding.Name, Message: msg + "; could not remove Fixora-created RoleBinding."}
		}
	}
	bindings, e := r.ListRoleBindings(cctx, p.Namespace)
	if e != nil {
		return &Outcome{Status: "cleanup-incomplete", Namespace: p.Namespace, Role: p.Role.Name, RoleBinding: bindingName, Message: msg + "; could not verify RoleBinding references, so the Role was retained."}
	}
	for _, b := range bindings {
		if b.RoleRef.Kind == "Role" && b.RoleRef.Name == p.Role.Name && b.UID != bindingUID {
			return &Outcome{Status: "cleanup-incomplete", Namespace: p.Namespace, Role: p.Role.Name, RoleBinding: bindingName, Message: msg + "; another RoleBinding references the Role, so it was retained."}
		}
	}
	if e = r.DeleteRole(cctx, p.Namespace, p.Role.Name, roleUID); e != nil {
		status = "cleanup-incomplete"
		return &Outcome{Status: status, Namespace: p.Namespace, Role: p.Role.Name, RoleBinding: bindingName, Message: msg + "; the created Role could not be removed."}
	}
	return &Outcome{Status: status, Namespace: p.Namespace, Role: p.Role.Name, Message: msg + "; Fixora removed only objects created by this attempt."}
}
