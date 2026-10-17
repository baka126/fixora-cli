package pdbfix

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/fixora/kubectl-fixora/internal/kube"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"strings"
)

type Runtime interface {
	Reader
	PatchPDB(context.Context, string, string, []byte, bool) (*policyv1.PodDisruptionBudget, error)
}
type typedRuntime struct{ k *kube.TypedClient }

func NewClient(k *kube.TypedClient) (Runtime, error) {
	if k == nil || k.Clientset == nil {
		return nil, errors.New("PDB repairs require a typed Kubernetes client")
	}
	return &typedRuntime{k}, nil
}
func (c *typedRuntime) GetPDB(ctx context.Context, ns, name string) (*policyv1.PodDisruptionBudget, error) {
	return c.k.Clientset.PolicyV1().PodDisruptionBudgets(ns).Get(ctx, name, metav1.GetOptions{})
}
func (c *typedRuntime) ListPods(ctx context.Context, ns string) ([]corev1.Pod, error) {
	l, e := c.k.Clientset.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{})
	if e != nil {
		return nil, e
	}
	return l.Items, nil
}
func (c *typedRuntime) ListPDBs(ctx context.Context, ns string) ([]policyv1.PodDisruptionBudget, error) {
	l, e := c.k.Clientset.PolicyV1().PodDisruptionBudgets(ns).List(ctx, metav1.ListOptions{})
	if e != nil {
		return nil, e
	}
	return l.Items, nil
}
func (c *typedRuntime) ListHPAs(ctx context.Context, ns string) ([]autoscalingv2.HorizontalPodAutoscaler, error) {
	l, e := c.k.Clientset.AutoscalingV2().HorizontalPodAutoscalers(ns).List(ctx, metav1.ListOptions{})
	if e != nil {
		return nil, e
	}
	return l.Items, nil
}
func (c *typedRuntime) GetWorkload(ctx context.Context, ns, kind, name string) (map[string]any, error) {
	var obj any
	var e error
	switch strings.ToLower(kind) {
	case "deployment":
		kind = "Deployment"
		obj, e = c.k.Clientset.AppsV1().Deployments(ns).Get(ctx, name, metav1.GetOptions{})
	case "statefulset":
		kind = "StatefulSet"
		obj, e = c.k.Clientset.AppsV1().StatefulSets(ns).Get(ctx, name, metav1.GetOptions{})
	case "replicaset":
		kind = "ReplicaSet"
		obj, e = c.k.Clientset.AppsV1().ReplicaSets(ns).Get(ctx, name, metav1.GetOptions{})
	default:
		return nil, errors.New("unsupported PDB owner")
	}
	if e != nil {
		return nil, e
	}
	b, e := json.Marshal(obj)
	if e != nil {
		return nil, e
	}
	var w map[string]any
	if e = json.Unmarshal(b, &w); e != nil {
		return nil, e
	}
	w["apiVersion"] = "apps/v1"
	w["kind"] = kind
	return w, nil
}

type ownerReader struct {
	kube.Reader
	c *typedRuntime
}

func (r ownerReader) GetResource(ctx context.Context, ns, res string) (map[string]any, error) {
	kind, name, ok := strings.Cut(res, "/")
	if !ok {
		return nil, errors.New("exact owner required")
	}
	return r.c.GetWorkload(ctx, ns, kind, name)
}
func (c *typedRuntime) ResolveOwner(ctx context.Context, p corev1.Pod) (kube.WorkloadOwner, error) {
	b, e := json.Marshal(p)
	if e != nil {
		return kube.WorkloadOwner{}, e
	}
	var kp kube.Pod
	if e = json.Unmarshal(b, &kp); e != nil {
		return kube.WorkloadOwner{}, e
	}
	return kube.ResolveWorkloadOwner(ctx, ownerReader{Reader: c.k, c: c}, kp)
}
func (c *typedRuntime) PatchPDB(ctx context.Context, ns, name string, b []byte, dry bool) (*policyv1.PodDisruptionBudget, error) {
	opts := metav1.PatchOptions{FieldManager: "fixora-pdb-repair"}
	if dry {
		opts.DryRun = []string{metav1.DryRunAll}
	}
	return c.k.Clientset.PolicyV1().PodDisruptionBudgets(ns).Patch(ctx, name, types.JSONPatchType, b, opts)
}
