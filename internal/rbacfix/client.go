package rbacfix

import (
	"context"
	"errors"
	"fmt"

	"github.com/fixora/kubectl-fixora/internal/kube"
	authnv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
)

type client struct{ k *kube.TypedClient }

func NewClient(k *kube.TypedClient) (Runtime, error) {
	if k == nil || k.Clientset == nil || k.Discovery == nil {
		return nil, errors.New("RBAC repair requires a typed Kubernetes client")
	}
	return &client{k}, nil
}
func (c *client) GetNamespace(ctx context.Context, n string) (*corev1.Namespace, error) {
	return c.k.Clientset.CoreV1().Namespaces().Get(ctx, n, metav1.GetOptions{})
}
func (c *client) GetServiceAccount(ctx context.Context, ns, n string) (*corev1.ServiceAccount, error) {
	return c.k.Clientset.CoreV1().ServiceAccounts(ns).Get(ctx, n, metav1.GetOptions{})
}
func (c *client) GetRole(ctx context.Context, ns, n string) (*rbacv1.Role, error) {
	return c.k.Clientset.RbacV1().Roles(ns).Get(ctx, n, metav1.GetOptions{})
}
func (c *client) GetRoleBinding(ctx context.Context, ns, n string) (*rbacv1.RoleBinding, error) {
	return c.k.Clientset.RbacV1().RoleBindings(ns).Get(ctx, n, metav1.GetOptions{})
}
func (c *client) ListRoleBindings(ctx context.Context, ns string) ([]rbacv1.RoleBinding, error) {
	l, e := c.k.Clientset.RbacV1().RoleBindings(ns).List(ctx, metav1.ListOptions{})
	if e != nil {
		return nil, e
	}
	return l.Items, nil
}
func accessDecision(allowed, denied bool, reason, eval string) Decision {
	return Decision{Allowed: allowed, Denied: denied, Reason: reason, EvaluationError: eval}
}
func (c *client) CheckSelfAccess(ctx context.Context, q Request) (Decision, error) {
	r := &authnv1.SelfSubjectAccessReview{Spec: authnv1.SelfSubjectAccessReviewSpec{ResourceAttributes: &authnv1.ResourceAttributes{Namespace: q.Namespace, Verb: q.Verb, Group: q.APIGroup, Resource: q.Resource, Subresource: q.Subresource, Name: q.Name}}}
	out, e := c.k.Clientset.AuthorizationV1().SelfSubjectAccessReviews().Create(ctx, r, metav1.CreateOptions{})
	if e != nil {
		return Decision{}, e
	}
	s := out.Status
	return accessDecision(s.Allowed, s.Denied, s.Reason, s.EvaluationError), nil
}
func (c *client) CheckSubjectAccess(ctx context.Context, user string, groups []string, q Request) (Decision, error) {
	r := &authnv1.SubjectAccessReview{Spec: authnv1.SubjectAccessReviewSpec{User: user, Groups: groups, ResourceAttributes: &authnv1.ResourceAttributes{Namespace: q.Namespace, Verb: q.Verb, Group: q.APIGroup, Resource: q.Resource, Subresource: q.Subresource, Name: q.Name}}}
	out, e := c.k.Clientset.AuthorizationV1().SubjectAccessReviews().Create(ctx, r, metav1.CreateOptions{})
	if e != nil {
		return Decision{}, e
	}
	s := out.Status
	return accessDecision(s.Allowed, s.Denied, s.Reason, s.EvaluationError), nil
}
func (c *client) IsNamespacedResource(ctx context.Context, group, resource string) (bool, error) {
	groups, err := c.k.Discovery.ServerPreferredResources()
	if err != nil {
		return false, err
	}
	for _, list := range groups {
		gv, e := schema.ParseGroupVersion(list.GroupVersion)
		if e != nil {
			return false, e
		}
		if gv.Group != group {
			continue
		}
		for _, apiResource := range list.APIResources {
			if apiResource.Name == resource {
				return apiResource.Namespaced, nil
			}
		}
	}
	return false, fmt.Errorf("resource %s/%s was not found in API discovery", group, resource)
}
func (c *client) DryRunRole(ctx context.Context, r *rbacv1.Role) (*rbacv1.Role, error) {
	return c.k.Clientset.RbacV1().Roles(r.Namespace).Create(ctx, r, metav1.CreateOptions{DryRun: []string{metav1.DryRunAll}, FieldManager: "kubectl-fixora-rbac-repair"})
}
func (c *client) DryRunRoleBinding(ctx context.Context, r *rbacv1.RoleBinding) (*rbacv1.RoleBinding, error) {
	return c.k.Clientset.RbacV1().RoleBindings(r.Namespace).Create(ctx, r, metav1.CreateOptions{DryRun: []string{metav1.DryRunAll}, FieldManager: "kubectl-fixora-rbac-repair"})
}
func (c *client) CreateRole(ctx context.Context, r *rbacv1.Role) (*rbacv1.Role, error) {
	return c.k.Clientset.RbacV1().Roles(r.Namespace).Create(ctx, r, metav1.CreateOptions{FieldManager: "kubectl-fixora-rbac-repair"})
}
func (c *client) CreateRoleBinding(ctx context.Context, r *rbacv1.RoleBinding) (*rbacv1.RoleBinding, error) {
	return c.k.Clientset.RbacV1().RoleBindings(r.Namespace).Create(ctx, r, metav1.CreateOptions{FieldManager: "kubectl-fixora-rbac-repair"})
}
func (c *client) DeleteRole(ctx context.Context, ns, n string, uid types.UID) error {
	o := metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid}}
	return c.k.Clientset.RbacV1().Roles(ns).Delete(ctx, n, o)
}
func (c *client) DeleteRoleBinding(ctx context.Context, ns, n string, uid types.UID) error {
	o := metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid}}
	return c.k.Clientset.RbacV1().RoleBindings(ns).Delete(ctx, n, o)
}
