package rbacfix

import (
	"context"
	"errors"
)

// ImportGrant validates an exported receipt against the current cluster before
// adding it to the local store. It reads exactly the receipt-bound Role and
// RoleBinding and performs no Kubernetes writes.
func ImportGrant(ctx context.Context, runtime Runtime, store *GrantStore, path string) (GrantSummary, error) {
	if runtime == nil || store == nil {
		return GrantSummary{}, errors.New("receipt import requires a cluster reader and local store")
	}
	receipt, err := store.ReadExport(path)
	if err != nil {
		return GrantSummary{}, err
	}
	role, err := runtime.GetRole(ctx, receipt.Namespace, receipt.Role.Name)
	if err != nil {
		return GrantSummary{}, errors.New("could not read the receipt-bound Role; no local receipt was saved")
	}
	if role.UID != receipt.Role.UID || !sameRole(receipt.Role, role) || gitOpsManaged(role.Labels, role.Annotations) {
		return GrantSummary{}, errors.New("receipt-bound Role identity, spec, or ownership changed; no local receipt was saved")
	}
	binding, err := runtime.GetRoleBinding(ctx, receipt.Namespace, receipt.RoleBinding.Name)
	if err != nil {
		return GrantSummary{}, errors.New("could not read the receipt-bound RoleBinding; no local receipt was saved")
	}
	if binding.UID != receipt.RoleBinding.UID || !sameRoleBinding(receipt.RoleBinding, binding) || gitOpsManaged(binding.Labels, binding.Annotations) {
		return GrantSummary{}, errors.New("receipt-bound RoleBinding identity, subject, RoleRef, or ownership changed; no local receipt was saved")
	}
	if err := store.Import(receipt); err != nil {
		return GrantSummary{}, err
	}
	return GrantSummary{ID: receipt.ID, Namespace: receipt.Namespace, ServiceAccount: receipt.ServiceAccount, CreatedAt: receipt.CreatedAt, Role: receipt.Role.Name, RoleBinding: receipt.RoleBinding.Name}, nil
}
