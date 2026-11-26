package rbacfix

import (
	"context"
	"errors"
	"fmt"

	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
)

func Prepare(ctx context.Context, r Runtime, a *Authority) (*Plan, error) {
	if r == nil {
		return nil, errors.New("RBAC repair runtime is unavailable")
	}
	if err := a.CheckFresh(); err != nil {
		return nil, err
	}
	ns := a.Namespace()
	if len(validation.IsDNS1123Label(ns)) > 0 {
		return nil, errors.New("policy namespace must be a valid namespace")
	}
	namespace, err := r.GetNamespace(ctx, ns)
	if err != nil {
		return nil, fmt.Errorf("read namespace: %w", err)
	}
	if gitOpsManaged(namespace.Labels, namespace.Annotations) {
		return nil, errors.New("namespace has GitOps/Helm ownership markers; update its source of truth instead")
	}
	sa, err := r.GetServiceAccount(ctx, ns, a.ServiceAccount())
	if err != nil {
		return nil, fmt.Errorf("read ServiceAccount %s/%s: %w", ns, a.ServiceAccount(), err)
	}
	if gitOpsManaged(sa.Labels, sa.Annotations) {
		return nil, errors.New("ServiceAccount has GitOps/Helm ownership markers; update its source of truth instead")
	}
	role, rb, err := a.Candidate()
	if err != nil {
		return nil, err
	}
	if _, err = r.GetRole(ctx, ns, role.Name); err == nil {
		return nil, fmt.Errorf("Role %s/%s already exists; Fixora will not adopt or modify it", ns, role.Name)
	} else if !apierrors.IsNotFound(err) {
		return nil, fmt.Errorf("check Role name availability: %w", err)
	}
	if _, err = r.GetRoleBinding(ctx, ns, rb.Name); err == nil {
		return nil, fmt.Errorf("RoleBinding %s/%s already exists; Fixora will not adopt or modify it", ns, rb.Name)
	} else if !apierrors.IsNotFound(err) {
		return nil, fmt.Errorf("check RoleBinding name availability: %w", err)
	}
	for _, q := range []Request{{Namespace: ns, APIGroup: "rbac.authorization.k8s.io", Resource: "roles", Verb: "create"}, {Namespace: ns, APIGroup: "rbac.authorization.k8s.io", Resource: "roles", Verb: "get"}, {Namespace: ns, APIGroup: "rbac.authorization.k8s.io", Resource: "roles", Verb: "delete"}, {Namespace: ns, APIGroup: "rbac.authorization.k8s.io", Resource: "rolebindings", Verb: "create"}, {Namespace: ns, APIGroup: "rbac.authorization.k8s.io", Resource: "rolebindings", Verb: "get"}, {Namespace: ns, APIGroup: "rbac.authorization.k8s.io", Resource: "rolebindings", Verb: "list"}, {Namespace: ns, APIGroup: "rbac.authorization.k8s.io", Resource: "rolebindings", Verb: "delete"}} {
		d, e := r.CheckSelfAccess(ctx, q)
		if e != nil {
			return nil, fmt.Errorf("check caller authorization for %s %s: %w", q.Verb, q.Resource, e)
		}
		if d.EvaluationError != "" || !d.Allowed {
			return nil, fmt.Errorf("caller is not confirmed authorized to %s %s in namespace %s", q.Verb, q.Resource, ns)
		}
	}
	requests, caveats := Requests(a)
	discovered := map[string]bool{}
	for _, q := range requests {
		discoveryResource := q.Resource
		if q.Subresource != "" {
			discoveryResource += "/" + q.Subresource
		}
		discoveryKey := q.APIGroup + "\x00" + discoveryResource
		if discovered[discoveryKey] {
			continue
		}
		discovered[discoveryKey] = true
		namespaced, e := r.IsNamespacedResource(ctx, q.APIGroup, discoveryResource)
		if e != nil {
			return nil, fmt.Errorf("discover permission target %s/%s: %w", q.APIGroup, discoveryResource, e)
		}
		if !namespaced {
			return nil, fmt.Errorf("permission target %s/%s is not namespaced; Role repair cannot grant it", q.APIGroup, discoveryResource)
		}
	}
	user := "system:serviceaccount:" + ns + ":" + a.ServiceAccount()
	groups := []string{"system:serviceaccounts", "system:serviceaccounts:" + ns, "system:authenticated"}
	decisions := make([]Decision, 0, len(requests))
	for _, q := range requests {
		d, e := r.CheckSubjectAccess(ctx, user, groups, q)
		if e != nil {
			return nil, fmt.Errorf("SubjectAccessReview %s %s: %w", q.Verb, q.Resource, e)
		}
		if d.EvaluationError != "" || (!d.Denied && !d.Allowed) {
			return nil, fmt.Errorf("authorization result is inconclusive for %s %s", q.Verb, q.Resource)
		}
		if d.Allowed {
			return nil, fmt.Errorf("policy includes permission already granted: %s %s %s", q.Verb, q.APIGroup, q.Resource)
		}
		decisions = append(decisions, d)
	}
	admittedRole, e := r.DryRunRole(ctx, role)
	if e != nil {
		return nil, fmt.Errorf("Role server-side dry-run refused: %w", e)
	}
	if !sameRole(role, admittedRole) {
		return nil, errors.New("admission changed the proposed Role; refusing to apply")
	}
	admittedRB, e := r.DryRunRoleBinding(ctx, rb)
	if e != nil {
		return nil, fmt.Errorf("RoleBinding server-side dry-run refused: %w", e)
	}
	if !sameRoleBinding(rb, admittedRB) {
		return nil, errors.New("admission changed the proposed RoleBinding; refusing to apply")
	}
	return &Plan{Namespace: ns, ServiceAccount: a.ServiceAccount(), Role: role, RoleBinding: rb, Requests: requests, Decisions: decisions, Caveats: caveats, serviceAccountUID: sa.UID, serviceAccountRV: sa.ResourceVersion, digest: a.Digest()}, nil
}

func gitOpsManaged(labels, annotations map[string]string) bool {
	for _, m := range []map[string]string{labels, annotations} {
		for k, v := range m {
			switch k {
			case "meta.helm.sh/release-name", "helm.sh/chart", "argocd.argoproj.io/instance", "argocd.argoproj.io/tracking-id", "kustomize.toolkit.fluxcd.io/name", "helm.toolkit.fluxcd.io/name":
				if v != "" {
					return true
				}
			case "app.kubernetes.io/managed-by":
				if v != "" && v != "fixora" {
					return true
				}
			}
		}
	}
	return false
}

func sameMeta(a, b metav1.Object) bool {
	return a.GetName() == b.GetName() && a.GetNamespace() == b.GetNamespace() && equalMap(a.GetLabels(), b.GetLabels()) && equalMap(a.GetAnnotations(), b.GetAnnotations())
}
func equalMap(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}
func sameRole(a, b *rbacv1.Role) bool {
	return a != nil && b != nil && sameMeta(a, b) && len(a.Rules) == len(b.Rules) && rulesEqual(a.Rules, b.Rules)
}
func rulesEqual(a, b []rbacv1.PolicyRule) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !slicesEqual(a[i].APIGroups, b[i].APIGroups) || !slicesEqual(a[i].Resources, b[i].Resources) || !slicesEqual(a[i].Verbs, b[i].Verbs) || !slicesEqual(a[i].ResourceNames, b[i].ResourceNames) || !slicesEqual(a[i].NonResourceURLs, b[i].NonResourceURLs) {
			return false
		}
	}
	return true
}
func slicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
func sameRoleBinding(a, b *rbacv1.RoleBinding) bool {
	if a == nil || b == nil || !sameMeta(a, b) || a.RoleRef != b.RoleRef || len(a.Subjects) != len(b.Subjects) {
		return false
	}
	for i := range a.Subjects {
		if a.Subjects[i] != b.Subjects[i] {
			return false
		}
	}
	return true
}
