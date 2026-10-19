package hpafix

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/fixora/kubectl-fixora/internal/kube"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
)

type Runtime interface {
	Reader
	PatchWorkload(context.Context, string, string, string, []byte, bool) (map[string]any, error)
}
type typedRuntime struct{ k *kube.TypedClient }

func NewClient(k *kube.TypedClient) (Runtime, error) {
	if k == nil || k.Clientset == nil || k.Dynamic == nil {
		return nil, errors.New("HPA repairs require typed Kubernetes and metrics clients")
	}
	return &typedRuntime{k: k}, nil
}
func (c *typedRuntime) GetHPA(ctx context.Context, ns, name string) (*autoscalingv2.HorizontalPodAutoscaler, error) {
	return c.k.Clientset.AutoscalingV2().HorizontalPodAutoscalers(ns).Get(ctx, name, metav1.GetOptions{})
}
func (c *typedRuntime) ListPods(ctx context.Context, ns string) ([]corev1.Pod, error) {
	list, err := c.k.Clientset.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	return list.Items, nil
}
func (c *typedRuntime) ListHPAs(ctx context.Context, ns string) ([]autoscalingv2.HorizontalPodAutoscaler, error) {
	list, err := c.k.Clientset.AutoscalingV2().HorizontalPodAutoscalers(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	return list.Items, nil
}
func (c *typedRuntime) ListPodMetrics(ctx context.Context, ns string) ([]map[string]any, error) {
	gvr := schema.GroupVersionResource{Group: "metrics.k8s.io", Version: "v1beta1", Resource: "pods"}
	list, err := c.k.Dynamic.Resource(gvr).Namespace(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(list.Items))
	for i := range list.Items {
		out = append(out, list.Items[i].Object)
	}
	return out, nil
}
func (c *typedRuntime) GetWorkload(ctx context.Context, ns, kind, name string) (map[string]any, error) {
	var obj any
	var err error
	switch strings.ToLower(kind) {
	case "deployment":
		kind = "Deployment"
		obj, err = c.k.Clientset.AppsV1().Deployments(ns).Get(ctx, name, metav1.GetOptions{})
	case "statefulset":
		kind = "StatefulSet"
		obj, err = c.k.Clientset.AppsV1().StatefulSets(ns).Get(ctx, name, metav1.GetOptions{})
	default:
		return nil, errors.New("unsupported HPA target kind")
	}
	if err != nil {
		return nil, err
	}
	b, err := json.Marshal(obj)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err = json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	m["apiVersion"] = "apps/v1"
	m["kind"] = kind
	return m, nil
}
func (c *typedRuntime) ResolveOwner(ctx context.Context, p corev1.Pod) (kube.WorkloadOwner, error) {
	b, err := json.Marshal(p)
	if err != nil {
		return kube.WorkloadOwner{}, err
	}
	var kp kube.Pod
	if err = json.Unmarshal(b, &kp); err != nil {
		return kube.WorkloadOwner{}, err
	}
	return kube.ResolveWorkloadOwner(ctx, hpaOwnerReader{Reader: c.k, k: c.k}, kp)
}

type hpaOwnerReader struct {
	kube.Reader
	k *kube.TypedClient
}

func (r hpaOwnerReader) GetResource(ctx context.Context, ns, res string) (map[string]any, error) {
	kind, name, ok := strings.Cut(res, "/")
	if !ok {
		return nil, errors.New("exact workload owner required")
	}
	c := &typedRuntime{k: r.k}
	return c.GetWorkload(ctx, ns, kind, name)
}
func (c *typedRuntime) PatchWorkload(ctx context.Context, ns, kind, name string, patch []byte, dry bool) (map[string]any, error) {
	opts := metav1.PatchOptions{FieldManager: "fixora-hpa-repair"}
	if dry {
		opts.DryRun = []string{metav1.DryRunAll}
	}
	var obj any
	var err error
	switch strings.ToLower(kind) {
	case "deployment":
		obj, err = c.k.Clientset.AppsV1().Deployments(ns).Patch(ctx, name, types.JSONPatchType, patch, opts)
	case "statefulset":
		obj, err = c.k.Clientset.AppsV1().StatefulSets(ns).Patch(ctx, name, types.JSONPatchType, patch, opts)
	default:
		return nil, errors.New("unsupported HPA target kind")
	}
	if err != nil {
		return nil, err
	}
	b, err := json.Marshal(obj)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err = json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	m["apiVersion"] = "apps/v1"
	m["kind"] = kind
	return m, nil
}
