package networkpolicyfix

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/fixora/kubectl-fixora/internal/kube"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
)

// Client adapts Fixora's required typed client-go stack to the strict policy
// lifecycle. No kubectl fallback is used for mutation or evidence collection.
type Client struct{ typed *kube.TypedClient }

func NewClient(typed *kube.TypedClient) (*Client, error) {
	if typed == nil || typed.Clientset == nil || typed.Dynamic == nil || typed.Config == nil {
		return nil, errors.New("complete typed Kubernetes client required")
	}
	return &Client{typed: typed}, nil
}

func (c *Client) GetPolicy(ctx context.Context, ns, name string) (*networkingv1.NetworkPolicy, error) {
	return c.typed.Clientset.NetworkingV1().NetworkPolicies(ns).Get(ctx, name, metav1.GetOptions{})
}
func (c *Client) ListPolicies(ctx context.Context, ns string) ([]networkingv1.NetworkPolicy, error) {
	var all []networkingv1.NetworkPolicy
	var token, rv string
	for {
		list, err := c.typed.Clientset.NetworkingV1().NetworkPolicies(ns).List(ctx, metav1.ListOptions{Limit: 500, Continue: token, ResourceVersion: rv})
		if err != nil {
			return nil, err
		}
		if rv == "" {
			rv = list.ResourceVersion
		}
		all = append(all, list.Items...)
		token = list.Continue
		if token == "" {
			return all, nil
		}
	}
}
func (c *Client) ListPods(ctx context.Context, ns string) ([]corev1.Pod, error) {
	var all []corev1.Pod
	var token, rv string
	for {
		list, err := c.typed.Clientset.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{Limit: 500, Continue: token, ResourceVersion: rv})
		if err != nil {
			return nil, err
		}
		if rv == "" {
			rv = list.ResourceVersion
		}
		all = append(all, list.Items...)
		token = list.Continue
		if token == "" {
			return all, nil
		}
	}
}
func (c *Client) GetNamespace(ctx context.Context, name string) (*corev1.Namespace, error) {
	return c.typed.Clientset.CoreV1().Namespaces().Get(ctx, name, metav1.GetOptions{})
}

func (c *Client) GetWorkload(ctx context.Context, kind, ns, name string) (map[string]any, error) {
	switch kind {
	case "Deployment":
		obj, err := c.typed.Clientset.AppsV1().Deployments(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return nil, err
		}
		return toMap(obj)
	case "StatefulSet":
		obj, err := c.typed.Clientset.AppsV1().StatefulSets(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return nil, err
		}
		return toMap(obj)
	case "DaemonSet":
		obj, err := c.typed.Clientset.AppsV1().DaemonSets(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return nil, err
		}
		return toMap(obj)
	default:
		return nil, errors.New("unsupported workload kind")
	}
}

func toMap(obj any) (map[string]any, error) {
	b, err := json.Marshal(obj)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	err = json.Unmarshal(b, &m)
	return m, err
}

func (c *Client) ResolveOwner(ctx context.Context, pod corev1.Pod) (kube.WorkloadOwner, error) {
	out := kube.WorkloadOwner{Kind: "Pod", Name: pod.Name, UID: string(pod.UID), Chain: []string{"Pod/" + pod.Name}}
	meta := pod.ObjectMeta
	child := "Pod"
	seen := map[string]bool{}
	for depth := 0; depth < 8; depth++ {
		var ref *metav1.OwnerReference
		for i := range meta.OwnerReferences {
			r := &meta.OwnerReferences[i]
			if r.Controller == nil || !*r.Controller {
				continue
			}
			if ref != nil {
				return out, errors.New("multiple controlling owners")
			}
			ref = r
		}
		if ref == nil {
			return out, nil
		}
		if ref.UID == "" || ref.Name == "" {
			return out, errors.New("owner identity incomplete")
		}
		key := ref.Kind + "/" + ref.Name
		if seen[key] {
			return out, errors.New("owner cycle")
		}
		seen[key] = true
		if child == "Pod" && ref.Kind != "ReplicaSet" && ref.Kind != "StatefulSet" && ref.Kind != "DaemonSet" || child == "ReplicaSet" && ref.Kind != "Deployment" {
			return out, fmt.Errorf("unsupported owner edge %s -> %s", child, ref.Kind)
		}
		obj, err := c.getController(ctx, pod.Namespace, ref.Kind, ref.Name)
		if err != nil {
			return out, err
		}
		m := mapAt(obj, "metadata")
		uid, _ := m["uid"].(string)
		name, _ := m["name"].(string)
		ns, _ := m["namespace"].(string)
		if uid != string(ref.UID) || name != ref.Name || ns != pod.Namespace || obj["kind"] != ref.Kind {
			return out, errors.New("owner identity changed")
		}
		out.Identities = append(out.Identities, kube.OwnerIdentity{Kind: ref.Kind, Name: ref.Name, UID: uid})
		out.Kind, out.Name, out.UID, out.Object = ref.Kind, ref.Name, uid, obj
		out.Chain = append(out.Chain, key)
		meta = metav1.ObjectMeta{}
		raw, _ := json.Marshal(m)
		_ = json.Unmarshal(raw, &meta)
		child = ref.Kind
	}
	return out, errors.New("owner traversal limit exceeded")
}

func (c *Client) getController(ctx context.Context, namespace, kind, name string) (map[string]any, error) {
	resource := ""
	switch kind {
	case "ReplicaSet":
		resource = "replicasets"
	case "Deployment":
		resource = "deployments"
	case "StatefulSet":
		resource = "statefulsets"
	case "DaemonSet":
		resource = "daemonsets"
	default:
		return nil, errors.New("unsupported controller owner")
	}
	obj, err := c.typed.Dynamic.Resource(schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: resource}).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	return obj.Object, nil
}

func (c *Client) AdmitPolicy(ctx context.Context, p *networkingv1.NetworkPolicy) (*networkingv1.NetworkPolicy, error) {
	copy := p.DeepCopy()
	return c.typed.Clientset.NetworkingV1().NetworkPolicies(p.Namespace).Update(ctx, copy, metav1.UpdateOptions{DryRun: []string{metav1.DryRunAll}, FieldManager: "fixora-networkpolicy-preview"})
}

func (c *Client) PatchSpec(ctx context.Context, ns, name string, uid, resourceVersion string, spec networkingv1.NetworkPolicySpec, dryRun bool) (*networkingv1.NetworkPolicy, error) {
	if uid == "" || resourceVersion == "" {
		return nil, errors.New("policy UID and resourceVersion preconditions required")
	}
	encodedSpec, err := json.Marshal(spec)
	if err != nil {
		return nil, err
	}
	ops := []map[string]any{{"op": "test", "path": "/metadata/uid", "value": uid}, {"op": "test", "path": "/metadata/resourceVersion", "value": resourceVersion}, {"op": "replace", "path": "/spec", "value": json.RawMessage(encodedSpec)}}
	body, err := json.Marshal(ops)
	if err != nil {
		return nil, err
	}
	options := metav1.PatchOptions{FieldManager: "fixora-networkpolicy-repair"}
	if dryRun {
		options.DryRun = []string{metav1.DryRunAll}
	}
	return c.typed.Clientset.NetworkingV1().NetworkPolicies(ns).Patch(ctx, name, types.JSONPatchType, body, options)
}
func (c *Client) CreateNamespace(ctx context.Context, n *corev1.Namespace) (*corev1.Namespace, error) {
	return c.typed.Clientset.CoreV1().Namespaces().Create(ctx, n, metav1.CreateOptions{FieldManager: "fixora-networkpolicy-verifier"})
}
func (c *Client) CreatePolicy(ctx context.Context, p *networkingv1.NetworkPolicy) (*networkingv1.NetworkPolicy, error) {
	return c.typed.Clientset.NetworkingV1().NetworkPolicies(p.Namespace).Create(ctx, p, metav1.CreateOptions{FieldManager: "fixora-networkpolicy-verifier"})
}
func (c *Client) CreatePod(ctx context.Context, p *corev1.Pod) (*corev1.Pod, error) {
	return c.typed.Clientset.CoreV1().Pods(p.Namespace).Create(ctx, p, metav1.CreateOptions{FieldManager: "fixora-networkpolicy-verifier"})
}
func (c *Client) ReadNamespace(ctx context.Context, n string) (*corev1.Namespace, error) {
	return c.GetNamespace(ctx, n)
}
func (c *Client) ReadPolicy(ctx context.Context, ns, n string) (*networkingv1.NetworkPolicy, error) {
	return c.GetPolicy(ctx, ns, n)
}
func (c *Client) ReadPod(ctx context.Context, ns, n string) (*corev1.Pod, error) {
	return c.typed.Clientset.CoreV1().Pods(ns).Get(ctx, n, metav1.GetOptions{})
}
func (c *Client) FindAttempt(ctx context.Context, ns, n, marker string) ([]ObjectRef, error) {
	var out []ObjectRef
	if ns == "" {
		obj, e := c.ReadNamespace(ctx, n)
		if e != nil {
			return nil, e
		}
		if obj.Annotations[runMarkerAnnotation] == marker {
			out = append(out, ObjectRef{Kind: "Namespace", Name: n, UID: string(obj.UID), RunMarker: marker})
		}
		return out, nil
	}
	if p, e := c.ReadPod(ctx, ns, n); e == nil && p.Annotations[runMarkerAnnotation] == marker {
		out = append(out, ObjectRef{Kind: "Pod", Namespace: ns, Name: n, UID: string(p.UID), RunMarker: marker})
	}
	if p, e := c.ReadPolicy(ctx, ns, n); e == nil && p.Annotations[runMarkerAnnotation] == marker {
		out = append(out, ObjectRef{Kind: "NetworkPolicy", Namespace: ns, Name: n, UID: string(p.UID), RunMarker: marker})
	}
	return out, nil
}
func (c *Client) PodLogs(ctx context.Context, ns, n string, limit int64) ([]byte, error) {
	request := c.typed.Clientset.CoreV1().Pods(ns).GetLogs(n, &corev1.PodLogOptions{Container: "probe", LimitBytes: &limit})
	stream, err := request.Stream(ctx)
	if err != nil {
		return nil, err
	}
	defer stream.Close()
	data, err := io.ReadAll(io.LimitReader(stream, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errors.New("probe output limit exceeded")
	}
	return data, nil
}
func (c *Client) DeleteOwned(ctx context.Context, ref ObjectRef) error {
	uid := types.UID(ref.UID)
	opts := metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid}}
	switch ref.Kind {
	case "Pod":
		obj, e := c.ReadPod(ctx, ref.Namespace, ref.Name)
		if e != nil {
			return e
		}
		if obj.Annotations[runMarkerAnnotation] != ref.RunMarker {
			return errors.New("Pod run marker changed")
		}
		return c.typed.Clientset.CoreV1().Pods(ref.Namespace).Delete(ctx, ref.Name, opts)
	case "NetworkPolicy":
		obj, e := c.ReadPolicy(ctx, ref.Namespace, ref.Name)
		if e != nil {
			return e
		}
		if obj.Annotations[runMarkerAnnotation] != ref.RunMarker {
			return errors.New("NetworkPolicy run marker changed")
		}
		return c.typed.Clientset.NetworkingV1().NetworkPolicies(ref.Namespace).Delete(ctx, ref.Name, opts)
	case "Namespace":
		obj, e := c.ReadNamespace(ctx, ref.Name)
		if e != nil {
			return e
		}
		if obj.Annotations[runMarkerAnnotation] != ref.RunMarker {
			return errors.New("Namespace run marker changed")
		}
		return c.typed.Clientset.CoreV1().Namespaces().Delete(ctx, ref.Name, opts)
	default:
		return errors.New("unsupported cleanup resource")
	}
}

var _ Runtime = (*Client)(nil)
