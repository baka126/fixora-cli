package debug

import (
	"context"
	"fmt"
	"strings"

	"github.com/fixora/kubectl-fixora/internal/kube"
)

type Result struct {
	Name     string   `json:"name"`
	Status   string   `json:"status"`
	Findings []string `json:"findings"`
	Next     []string `json:"next"`
}

func Trace(ctx context.Context, k kube.Kubectl, namespace, resource string) Result {
	r := Result{Name: "connectivity-trace", Status: "ok"}
	r.Findings = append(r.Findings, "Trace checks route/service/endpoints/pod relationships using local Kubernetes objects.")
	if resource == "" {
		r.Status = "needs-input"
		r.Next = append(r.Next, "Pass service/name, ingress/name, or httproute/name.")
		return r
	}
	obj, err := k.GetResource(ctx, namespace, resource)
	if err != nil {
		r.Status = "error"
		r.Findings = append(r.Findings, err.Error())
		return r
	}
	r.Findings = append(r.Findings, "Loaded "+kindName(obj, resource))
	if strings.HasPrefix(strings.ToLower(resource), "service/") {
		items, _ := k.GetResourceItems(ctx, namespace, false, "endpointslices")
		r.Findings = append(r.Findings, fmt.Sprintf("EndpointSlices in namespace: %d", len(items)))
	}
	r.Next = append(r.Next, "Check targetPort, selectors, EndpointSlices, readiness probes, and backend pod logs.")
	return r
}

func Storage(ctx context.Context, k kube.Kubectl, namespace string) Result {
	r := Result{Name: "storage", Status: "ok"}
	pvcs, err := k.GetResourceItems(ctx, namespace, namespace == "", "pvc")
	if err != nil {
		r.Status = "error"
		r.Findings = append(r.Findings, err.Error())
		return r
	}
	for _, pvc := range pvcs {
		status := mapString(pvc, "status", "phase")
		if status == "Pending" || status == "Lost" {
			r.Status = "warning"
			r.Findings = append(r.Findings, fmt.Sprintf("PVC %s is %s", meta(pvc), status))
		}
	}
	if len(r.Findings) == 0 {
		r.Findings = append(r.Findings, "No pending or lost PVCs found.")
	}
	r.Next = append(r.Next, "Inspect StorageClass, provisioner events, topology, quota, and access modes.")
	return r
}

func RBAC(ctx context.Context, k kube.Kubectl, namespace, serviceAccount, verb, resource string) Result {
	r := Result{Name: "rbac", Status: "ok"}
	if verb == "" {
		verb = "get"
	}
	if resource == "" {
		resource = "pods"
	}
	out, err := k.AuthCanI(ctx, namespace, serviceAccount, verb, resource)
	if err != nil {
		r.Status = "error"
		r.Findings = append(r.Findings, err.Error())
		return r
	}
	r.Findings = append(r.Findings, fmt.Sprintf("can %s %s: %s", verb, resource, out))
	if strings.TrimSpace(out) != "yes" {
		r.Status = "warning"
		r.Next = append(r.Next, "Create a least-privilege Role and RoleBinding for the service account.")
	}
	return r
}

func DNS(ctx context.Context, k kube.Kubectl, namespace string) Result {
	r := Result{Name: "dns", Status: "ok"}
	svcs, err := k.GetResourceItems(ctx, namespace, namespace == "", "services")
	if err != nil {
		r.Status = "error"
		r.Findings = append(r.Findings, err.Error())
		return r
	}
	r.Findings = append(r.Findings, fmt.Sprintf("Services discovered: %d", len(svcs)))
	coredns, _ := k.GetResourceItems(ctx, "kube-system", false, "pods")
	count := 0
	for _, pod := range coredns {
		if strings.Contains(strings.ToLower(meta(pod)), "coredns") {
			count++
		}
	}
	r.Findings = append(r.Findings, fmt.Sprintf("CoreDNS pods discovered: %d", count))
	r.Next = append(r.Next, "Validate service DNS name, endpoints, CoreDNS health, and NetworkPolicy egress.")
	return r
}

func Security(ctx context.Context, k kube.Kubectl, namespace string) Result {
	r := Result{Name: "security", Status: "ok"}
	events, err := k.GetEvents(ctx, namespace)
	if err != nil {
		r.Status = "error"
		r.Findings = append(r.Findings, err.Error())
		return r
	}
	for _, event := range events {
		msg := strings.ToLower(event.Message)
		if strings.Contains(msg, "permission denied") || strings.Contains(msg, "podsecurity") || strings.Contains(msg, "forbidden") || strings.Contains(msg, "seccomp") || strings.Contains(msg, "apparmor") || strings.Contains(msg, "read-only") {
			r.Status = "warning"
			r.Findings = append(r.Findings, event.Reason+": "+event.Message)
		}
	}
	if len(r.Findings) == 0 {
		r.Findings = append(r.Findings, "No obvious security-policy event failures found.")
	}
	r.Next = append(r.Next, "Check PodSecurity, Kyverno/Gatekeeper, securityContext, readOnlyRootFilesystem, runAsUser, and volume mounts.")
	return r
}

func NodePressure(ctx context.Context, k kube.Kubectl) Result {
	r := Result{Name: "node-pressure", Status: "ok"}
	nodes, err := k.GetNodes(ctx)
	if err != nil {
		r.Status = "error"
		r.Findings = append(r.Findings, err.Error())
		return r
	}
	for _, node := range nodes {
		for _, cond := range node.Status.Conditions {
			if cond.Status == "True" && strings.Contains(cond.Type, "Pressure") {
				r.Status = "warning"
				r.Findings = append(r.Findings, fmt.Sprintf("%s has %s", node.Metadata.Name, cond.Type))
			}
			if cond.Type == "Ready" && cond.Status != "True" {
				r.Status = "warning"
				r.Findings = append(r.Findings, fmt.Sprintf("%s Ready=%s", node.Metadata.Name, cond.Status))
			}
		}
	}
	if len(r.Findings) == 0 {
		r.Findings = append(r.Findings, "No obvious node pressure conditions found.")
	}
	r.Next = append(r.Next, "Correlate failing pods with node taints, pressure conditions, evictions, and architecture labels.")
	return r
}

func kindName(obj map[string]any, fallback string) string {
	kind, _ := obj["kind"].(string)
	if kind == "" {
		kind = fallback
	}
	return kind + "/" + meta(obj)
}

func meta(obj map[string]any) string {
	m, _ := obj["metadata"].(map[string]any)
	return fmt.Sprint(m["name"])
}

func mapString(obj map[string]any, keys ...string) string {
	var cur any = obj
	for _, key := range keys {
		m, ok := cur.(map[string]any)
		if !ok {
			return ""
		}
		cur = m[key]
	}
	if cur == nil {
		return ""
	}
	return fmt.Sprint(cur)
}
