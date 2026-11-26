package rbacfix

import (
	"context"
	"errors"
	"fmt"
	"reflect"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
)

type RevokePlan struct {
	Receipt               GrantReceipt `json:"receipt"`
	BindingPresent        bool         `json:"bindingPresent"`
	RolePresent           bool         `json:"rolePresent"`
	SharedRole            bool         `json:"sharedRole"`
	ServiceAccountChanged bool         `json:"serviceAccountChanged"`
}

func PrepareRevoke(ctx context.Context, r Runtime, s *GrantStore, id, namespace string) (*RevokePlan, error) {
	if namespace == "" {
		return nil, errors.New("an explicit namespace is required")
	}
	receipt, err := s.Get(id)
	if err != nil {
		return nil, err
	}
	if receipt.Namespace != namespace {
		return nil, errors.New("namespace does not match grant receipt")
	}
	for _, q := range []Request{{Namespace: namespace, APIGroup: "rbac.authorization.k8s.io", Resource: "rolebindings", Verb: "get"}, {Namespace: namespace, APIGroup: "rbac.authorization.k8s.io", Resource: "rolebindings", Verb: "list"}, {Namespace: namespace, APIGroup: "rbac.authorization.k8s.io", Resource: "rolebindings", Verb: "delete"}, {Namespace: namespace, APIGroup: "rbac.authorization.k8s.io", Resource: "roles", Verb: "get"}, {Namespace: namespace, APIGroup: "rbac.authorization.k8s.io", Resource: "roles", Verb: "delete"}, {APIGroup: "authorization.k8s.io", Resource: "selfsubjectaccessreviews", Verb: "create"}, {APIGroup: "authorization.k8s.io", Resource: "subjectaccessreviews", Verb: "create"}} {
		d, e := r.CheckSelfAccess(ctx, q)
		if e != nil || !d.Allowed || d.EvaluationError != "" {
			return nil, fmt.Errorf("caller authorization is not confirmed for %s %s", q.Verb, q.Resource)
		}
	}
	p := &RevokePlan{Receipt: receipt}
	b, e := r.GetRoleBinding(ctx, namespace, receipt.RoleBinding.Name)
	if e == nil {
		if b.UID != receipt.RoleBinding.UID || !sameRoleBinding(receipt.RoleBinding, b) || gitOpsManaged(b.Labels, b.Annotations) {
			return nil, errors.New("RoleBinding identity or ownership changed; refusing revoke")
		}
		p.BindingPresent = true
	} else if !apierrors.IsNotFound(e) {
		return nil, e
	}
	ro, e := r.GetRole(ctx, namespace, receipt.Role.Name)
	if e == nil {
		if ro.UID != receipt.Role.UID || !sameRole(receipt.Role, ro) || gitOpsManaged(ro.Labels, ro.Annotations) {
			return nil, errors.New("Role identity or ownership changed; refusing revoke")
		}
		p.RolePresent = true
	} else if !apierrors.IsNotFound(e) {
		return nil, e
	}
	if p.BindingPresent {
		if b.RoleRef != receipt.RoleBinding.RoleRef || !reflect.DeepEqual(b.Subjects, receipt.RoleBinding.Subjects) {
			return nil, errors.New("RoleBinding target changed; refusing revoke")
		}
	}
	bindings, e := r.ListRoleBindings(ctx, namespace)
	if e != nil {
		return nil, e
	}
	for _, x := range bindings {
		if x.RoleRef.Kind == "Role" && x.RoleRef.Name == receipt.Role.Name && x.UID != receipt.RoleBinding.UID {
			p.SharedRole = true
		}
	}
	sa, e := r.GetServiceAccount(ctx, namespace, receipt.ServiceAccount)
	if e == nil {
		p.ServiceAccountChanged = string(sa.UID) != receipt.ServiceAccountUID
	} else if !apierrors.IsNotFound(e) {
		return nil, e
	} else {
		p.ServiceAccountChanged = true
	}
	return p, nil
}

func Revoke(ctx context.Context, r Runtime, s *GrantStore, prepared *RevokePlan) (*Outcome, error) {
	if prepared == nil {
		return nil, errors.New("reviewed revoke plan is required")
	}
	id := prepared.Receipt.ID
	fresh, err := PrepareRevoke(ctx, r, s, id, prepared.Receipt.Namespace)
	if err != nil {
		return nil, err
	}
	if fresh.Receipt.Role.UID != prepared.Receipt.Role.UID || fresh.Receipt.RoleBinding.UID != prepared.Receipt.RoleBinding.UID || fresh.BindingPresent != prepared.BindingPresent || fresh.RolePresent != prepared.RolePresent || fresh.SharedRole != prepared.SharedRole {
		return nil, errors.New("grant or live RBAC state changed after preview; review a new preview")
	}
	rc := fresh.Receipt
	out := &Outcome{Namespace: rc.Namespace, Role: rc.Role.Name, RoleBinding: rc.RoleBinding.Name}
	if fresh.BindingPresent {
		e := r.DeleteRoleBinding(ctx, rc.Namespace, rc.RoleBinding.Name, types.UID(rc.RoleBinding.UID))
		if e != nil {
			b, re := r.GetRoleBinding(ctx, rc.Namespace, rc.RoleBinding.Name)
			if re == nil && b.UID == rc.RoleBinding.UID {
				out.Status = "revoke-uncertain"
				out.Message = "RoleBinding deletion is uncertain; the recorded binding remains. Retry after checking cluster state."
				return out, nil
			}
			if re == nil {
				out.Status = "revoke-blocked-replacement"
				out.Message = "A different RoleBinding now occupies the recorded name; it was left untouched."
				return out, nil
			}
			if !apierrors.IsNotFound(re) {
				out.Status = "revoke-uncertain"
				out.Message = "RoleBinding deletion could not be confirmed; receipt retained."
				return out, nil
			}
		}
	}
	bindings, e := r.ListRoleBindings(ctx, rc.Namespace)
	if e != nil {
		out.Status = "grant-revoked-role-retained"
		out.Message = "The recorded RoleBinding is gone, but Fixora could not inspect other references; Role retained and receipt kept."
		return out, nil
	}
	shared := false
	for _, b := range bindings {
		if b.RoleRef.Kind == "Role" && b.RoleRef.Name == rc.Role.Name {
			shared = true
			break
		}
	}
	if shared {
		out.Status = "grant-revoked-role-retained"
	} else {
		ro, re := r.GetRole(ctx, rc.Namespace, rc.Role.Name)
		if re == nil && ro.UID != rc.Role.UID {
			out.Status = "revoke-blocked-replacement"
			out.Message = "A different Role now occupies the recorded name; it was left untouched."
			return out, nil
		}
		if re != nil && !apierrors.IsNotFound(re) {
			out.Status = "revoke-uncertain"
			out.Message = "Could not verify the recorded Role; receipt retained."
			return out, nil
		}
		if re == nil {
			if de := r.DeleteRole(ctx, rc.Namespace, rc.Role.Name, types.UID(rc.Role.UID)); de != nil {
				check, ce := r.GetRole(ctx, rc.Namespace, rc.Role.Name)
				if ce == nil && check.UID == rc.Role.UID {
					out.Status = "revoke-uncertain"
					out.Message = "Role deletion is uncertain; receipt retained for retry."
					return out, nil
				}
				if ce == nil {
					out.Status = "revoke-blocked-replacement"
					out.Message = "A replacement Role was left untouched."
					return out, nil
				}
				if !apierrors.IsNotFound(ce) {
					out.Status = "revoke-uncertain"
					out.Message = "Role deletion could not be confirmed; receipt retained."
					return out, nil
				}
			}
		}
	}
	remaining := false
	for _, q := range rc.Requests {
		d, e := r.CheckSubjectAccess(ctx, "system:serviceaccount:"+rc.Namespace+":"+rc.ServiceAccount, []string{"system:serviceaccounts", "system:serviceaccounts:" + rc.Namespace, "system:authenticated"}, q)
		if e != nil || d.EvaluationError != "" {
			out.Status = "revoke-partial"
			out.Message = "Objects were removed but remaining authorization could not be confirmed; receipt retained."
			return out, nil
		}
		if d.Allowed {
			remaining = true
		}
	}
	if shared {
		out.Status = "grant-revoked-role-retained"
		out.Message = "The recorded RoleBinding was removed; a different RoleBinding still references the Role. The receipt was kept for follow-up."
		return out, nil
	}
	if e := s.Delete(id); e != nil {
		out.Status = "revoke-complete-receipt-retained"
		out.Message = "Recorded objects are absent, but the local receipt could not be removed."
		return out, nil
	}
	if remaining {
		out.Status = "grant-revoked-access-remains"
		out.Message = "The recorded grant objects were removed; other permissions still authorize at least one requested action."
	} else {
		out.Status = "grant-revoked-and-denied"
		out.Message = "The recorded grant objects were removed and the requested actions are now denied. Workload recovery was not evaluated."
	}
	return out, nil
}
