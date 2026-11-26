package rbacfix

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

type Request struct{ Namespace, APIGroup, Resource, Subresource, Verb, Name string }
type Decision struct {
	Allowed         bool   `json:"allowed"`
	Denied          bool   `json:"denied"`
	Reason          string `json:"reason,omitempty"`
	EvaluationError string `json:"evaluationError,omitempty"`
}
type Plan struct {
	Namespace         string              `json:"namespace"`
	ServiceAccount    string              `json:"serviceAccount"`
	Role              *rbacv1.Role        `json:"role"`
	RoleBinding       *rbacv1.RoleBinding `json:"roleBinding"`
	Requests          []Request           `json:"requests"`
	Decisions         []Decision          `json:"decisions"`
	Caveats           []string            `json:"caveats,omitempty"`
	serviceAccountUID types.UID
	serviceAccountRV  string
	digest            string
}
type Outcome struct {
	Status      string     `json:"status"`
	Namespace   string     `json:"namespace"`
	Role        string     `json:"role,omitempty"`
	RoleBinding string     `json:"roleBinding,omitempty"`
	Decisions   []Decision `json:"decisions,omitempty"`
	Message     string     `json:"message,omitempty"`
	GrantID     string     `json:"grantId,omitempty"`
	ReceiptPath string     `json:"receiptPath,omitempty"`
}

type Runtime interface {
	GetNamespace(context.Context, string) (*corev1.Namespace, error)
	GetServiceAccount(context.Context, string, string) (*corev1.ServiceAccount, error)
	GetRole(context.Context, string, string) (*rbacv1.Role, error)
	GetRoleBinding(context.Context, string, string) (*rbacv1.RoleBinding, error)
	ListRoleBindings(context.Context, string) ([]rbacv1.RoleBinding, error)
	CheckSelfAccess(context.Context, Request) (Decision, error)
	CheckSubjectAccess(context.Context, string, []string, Request) (Decision, error)
	IsNamespacedResource(context.Context, string, string) (bool, error)
	DryRunRole(context.Context, *rbacv1.Role) (*rbacv1.Role, error)
	DryRunRoleBinding(context.Context, *rbacv1.RoleBinding) (*rbacv1.RoleBinding, error)
	CreateRole(context.Context, *rbacv1.Role) (*rbacv1.Role, error)
	CreateRoleBinding(context.Context, *rbacv1.RoleBinding) (*rbacv1.RoleBinding, error)
	DeleteRole(context.Context, string, string, types.UID) error
	DeleteRoleBinding(context.Context, string, string, types.UID) error
}

func expectedLabels() map[string]string {
	return map[string]string{"app.kubernetes.io/managed-by": "fixora"}
}
func expectedAnnotations(digest string) map[string]string {
	return map[string]string{"fixora.io/rbac-policy-sha256": digest}
}

func (a *Authority) Candidate() (*rbacv1.Role, *rbacv1.RoleBinding, error) {
	if err := a.CheckFresh(); err != nil {
		return nil, nil, err
	}
	r := &rbacv1.Role{ObjectMeta: metav1.ObjectMeta{Name: a.policy.Spec.RoleName, Namespace: a.policy.Metadata.Namespace, Labels: expectedLabels(), Annotations: expectedAnnotations(a.Digest())}}
	for _, p := range a.policy.Spec.Permissions {
		parts := splitResource(p.Resource)
		rule := rbacv1.PolicyRule{APIGroups: []string{p.APIGroup}, Resources: []string{parts[0]}, Verbs: append([]string(nil), p.Verbs...)}
		if len(parts) > 1 {
			rule.Resources[0] = p.Resource
		}
		if len(p.ResourceNames) > 0 {
			rule.ResourceNames = append([]string(nil), p.ResourceNames...)
		}
		r.Rules = append(r.Rules, rule)
	}
	b := &rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{Name: a.policy.Spec.RoleBindingName, Namespace: a.policy.Metadata.Namespace, Labels: expectedLabels(), Annotations: expectedAnnotations(a.Digest())}, Subjects: []rbacv1.Subject{{Kind: "ServiceAccount", Name: a.policy.Spec.ServiceAccount, Namespace: a.policy.Metadata.Namespace}}, RoleRef: rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "Role", Name: a.policy.Spec.RoleName}}
	return r, b, nil
}

func splitResource(resource string) []string {
	for i, c := range resource {
		if c == '/' {
			return []string{resource[:i], resource[i+1:]}
		}
	}
	return []string{resource}
}

func Requests(a *Authority) ([]Request, []string) {
	var out []Request
	var caveats []string
	for _, p := range a.policy.Spec.Permissions {
		parts := splitResource(p.Resource)
		sub := ""
		if len(parts) > 1 {
			sub = parts[1]
		}
		names := p.ResourceNames
		if len(names) == 0 {
			names = []string{""}
		}
		for _, v := range p.Verbs {
			for _, n := range names {
				out = append(out, Request{Namespace: a.Namespace(), APIGroup: p.APIGroup, Resource: parts[0], Subresource: sub, Verb: v, Name: n})
				if n != "" && (v == "list" || v == "watch") {
					caveats = append(caveats, "list/watch on "+p.Resource+" requires a metadata.name field selector for each approved resource name")
				}
			}
		}
	}
	return out, caveats
}
