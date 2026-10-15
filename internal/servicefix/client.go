package servicefix

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/fixora/kubectl-fixora/internal/kube"
	"io"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"strings"
	"time"
)

type ObjectRef struct {
	Kind      string `json:"kind"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	UID       string `json:"uid"`
}
type Runtime interface {
	Reader
	Admit(context.Context, *corev1.Service) (*corev1.Service, error)
	CreateService(context.Context, *corev1.Service) (*corev1.Service, error)
	CreatePolicy(context.Context, *networkingv1.NetworkPolicy) (*networkingv1.NetworkPolicy, error)
	CreatePod(context.Context, *corev1.Pod) (*corev1.Pod, error)
	ReadPod(context.Context, string, string) (*corev1.Pod, error)
	ReadPolicy(context.Context, string, string) (*networkingv1.NetworkPolicy, error)
	FindAttempt(context.Context, string, string, string) ([]ObjectRef, error)
	PodLogs(context.Context, string, string, int64) ([]byte, error)
	DeleteOwned(context.Context, ObjectRef) error
	PatchService(context.Context, string, string, []byte) (*corev1.Service, error)
}
type typedRuntime struct{ k *kube.TypedClient }

func NewClient(k *kube.TypedClient) (Runtime, error) {
	if k == nil || k.Clientset == nil {
		return nil, errors.New("Service verification requires a typed Kubernetes client")
	}
	return &typedRuntime{k: k}, nil
}
func (c *typedRuntime) GetNamespace(ctx context.Context, ns string) (*corev1.Namespace, error) {
	return c.k.Clientset.CoreV1().Namespaces().Get(ctx, ns, metav1.GetOptions{})
}
func (c *typedRuntime) GetService(ctx context.Context, ns, name string) (*corev1.Service, error) {
	return c.k.Clientset.CoreV1().Services(ns).Get(ctx, name, metav1.GetOptions{})
}
func (c *typedRuntime) GetWorkload(ctx context.Context, ns, kind, name string) (map[string]any, error) {
	var obj any
	var err error
	switch strings.ToLower(kind) {
	case "deployment":
		obj, err = c.k.Clientset.AppsV1().Deployments(ns).Get(ctx, name, metav1.GetOptions{})
		kind = "Deployment"
	case "statefulset":
		obj, err = c.k.Clientset.AppsV1().StatefulSets(ns).Get(ctx, name, metav1.GetOptions{})
		kind = "StatefulSet"
	case "daemonset":
		obj, err = c.k.Clientset.AppsV1().DaemonSets(ns).Get(ctx, name, metav1.GetOptions{})
		kind = "DaemonSet"
	case "replicaset":
		obj, err = c.k.Clientset.AppsV1().ReplicaSets(ns).Get(ctx, name, metav1.GetOptions{})
		kind = "ReplicaSet"
	default:
		return nil, errors.New("unsupported backend ownership")
	}
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(obj)
	if err != nil {
		return nil, err
	}
	var result map[string]any
	if err = json.Unmarshal(raw, &result); err != nil {
		return nil, err
	}
	result["apiVersion"], result["kind"] = "apps/v1", kind
	return result, nil
}
func (c *typedRuntime) ListPods(ctx context.Context, ns string) ([]corev1.Pod, error) {
	list, err := c.k.Clientset.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	return list.Items, nil
}
func (c *typedRuntime) ListSlices(ctx context.Context, ns, name string) ([]discoveryv1.EndpointSlice, error) {
	list, err := c.k.Clientset.DiscoveryV1().EndpointSlices(ns).List(ctx, metav1.ListOptions{LabelSelector: labels.Set{"kubernetes.io/service-name": name}.AsSelector().String()})
	if err != nil {
		return nil, err
	}
	return list.Items, nil
}

type typedOwnerReader struct {
	kube.Reader
	c *typedRuntime
}

func (r typedOwnerReader) GetResource(ctx context.Context, ns, resource string) (map[string]any, error) {
	kind, name, ok := strings.Cut(resource, "/")
	if !ok {
		return nil, errors.New("exact owner resource required")
	}
	return r.c.GetWorkload(ctx, ns, kind, name)
}
func (c *typedRuntime) ResolveOwner(ctx context.Context, p corev1.Pod) (kube.WorkloadOwner, error) {
	b, _ := json.Marshal(p)
	var pod kube.Pod
	if json.Unmarshal(b, &pod) != nil {
		return kube.WorkloadOwner{}, errors.New("invalid backend Pod")
	}
	return kube.ResolveWorkloadOwner(ctx, typedOwnerReader{Reader: c.k, c: c}, pod)
}
func (c *typedRuntime) Admit(ctx context.Context, s *corev1.Service) (*corev1.Service, error) {
	return c.k.Clientset.CoreV1().Services(s.Namespace).Update(ctx, s.DeepCopy(), metav1.UpdateOptions{DryRun: []string{metav1.DryRunAll}, FieldManager: "fixora-service-verification"})
}
func (c *typedRuntime) CreateService(ctx context.Context, s *corev1.Service) (*corev1.Service, error) {
	return c.k.Clientset.CoreV1().Services(s.Namespace).Create(ctx, s.DeepCopy(), metav1.CreateOptions{})
}
func (c *typedRuntime) CreatePolicy(ctx context.Context, n *networkingv1.NetworkPolicy) (*networkingv1.NetworkPolicy, error) {
	return c.k.Clientset.NetworkingV1().NetworkPolicies(n.Namespace).Create(ctx, n.DeepCopy(), metav1.CreateOptions{})
}
func (c *typedRuntime) CreatePod(ctx context.Context, p *corev1.Pod) (*corev1.Pod, error) {
	return c.k.Clientset.CoreV1().Pods(p.Namespace).Create(ctx, p.DeepCopy(), metav1.CreateOptions{})
}
func (c *typedRuntime) ReadPod(ctx context.Context, ns, name string) (*corev1.Pod, error) {
	return c.k.Clientset.CoreV1().Pods(ns).Get(ctx, name, metav1.GetOptions{})
}
func (c *typedRuntime) PodLogs(ctx context.Context, ns, name string, limit int64) ([]byte, error) {
	if limit < 1 || limit > 64<<10 {
		return nil, errors.New("bounded probe logs required")
	}
	stream, err := c.k.Clientset.CoreV1().Pods(ns).GetLogs(name, &corev1.PodLogOptions{Container: "verifier", LimitBytes: &limit}).Stream(ctx)
	if err != nil {
		return nil, err
	}
	defer stream.Close()
	b, err := io.ReadAll(io.LimitReader(stream, limit+1))
	if err != nil || int64(len(b)) > limit {
		return nil, errors.New("probe logs incomplete or exceeded limit")
	}
	return b, nil
}
func (c *typedRuntime) DeleteOwned(ctx context.Context, ref ObjectRef) error {
	if ref.UID == "" || ref.Name == "" || ref.Namespace == "" {
		return errors.New("owned resource UID required")
	}
	uid := types.UID(ref.UID)
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
	}
	grace := int64(0)
	options := metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid}, GracePeriodSeconds: &grace}
	var got types.UID
	var err error
	switch ref.Kind {
	case "Service":
		o, e := c.GetService(ctx, ref.Namespace, ref.Name)
		err = e
		if o != nil {
			got = o.UID
		}
	case "Pod":
		o, e := c.ReadPod(ctx, ref.Namespace, ref.Name)
		err = e
		if o != nil {
			got = o.UID
		}
	case "NetworkPolicy":
		o, e := c.k.Clientset.NetworkingV1().NetworkPolicies(ref.Namespace).Get(ctx, ref.Name, metav1.GetOptions{})
		err = e
		if o != nil {
			got = o.UID
		}
	default:
		return errors.New("unsupported cleanup kind")
	}
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if got != uid {
		return errors.New("cleanup resource was replaced")
	}
	switch ref.Kind {
	case "Service":
		err = c.k.Clientset.CoreV1().Services(ref.Namespace).Delete(ctx, ref.Name, options)
	case "Pod":
		err = c.k.Clientset.CoreV1().Pods(ref.Namespace).Delete(ctx, ref.Name, options)
	case "NetworkPolicy":
		err = c.k.Clientset.NetworkingV1().NetworkPolicies(ref.Namespace).Delete(ctx, ref.Name, options)
	}
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for {
		identity, readErr := c.cleanupIdentity(ctx, ref)
		if apierrors.IsNotFound(readErr) {
			return nil
		}
		if readErr != nil {
			return readErr
		}
		if identity != uid {
			return errors.New("cleanup resource replaced before deletion completed")
		}
		if err = pause(ctx); err != nil {
			return err
		}
	}
}
func (c *typedRuntime) cleanupIdentity(ctx context.Context, ref ObjectRef) (types.UID, error) {
	switch ref.Kind {
	case "Service":
		o, err := c.GetService(ctx, ref.Namespace, ref.Name)
		if o != nil {
			return o.UID, err
		}
		return "", err
	case "Pod":
		o, err := c.ReadPod(ctx, ref.Namespace, ref.Name)
		if o != nil {
			return o.UID, err
		}
		return "", err
	case "NetworkPolicy":
		o, err := c.k.Clientset.NetworkingV1().NetworkPolicies(ref.Namespace).Get(ctx, ref.Name, metav1.GetOptions{})
		if o != nil {
			return o.UID, err
		}
		return "", err
	}
	return "", errors.New("unsupported cleanup kind")
}
func (c *typedRuntime) PatchService(ctx context.Context, ns, name string, patch []byte) (*corev1.Service, error) {
	return c.k.Clientset.CoreV1().Services(ns).Patch(ctx, name, types.JSONPatchType, patch, metav1.PatchOptions{FieldManager: "fixora-service-repair"})
}

func (c *typedRuntime) GetServiceDocument(ctx context.Context, ns, name string) ([]byte, error) {
	rest := c.k.Clientset.CoreV1().RESTClient()
	if rest == nil {
		return nil, errors.New("raw Service API unavailable")
	}
	return rest.Get().Namespace(ns).Resource("services").Name(name).Do(ctx).Raw()
}

func (c *typedRuntime) ReadPolicy(ctx context.Context, ns, name string) (*networkingv1.NetworkPolicy, error) {
	return c.k.Clientset.NetworkingV1().NetworkPolicies(ns).Get(ctx, name, metav1.GetOptions{})
}

func (c *typedRuntime) FindAttempt(ctx context.Context, kind, ns, nonce string) ([]ObjectRef, error) {
	if ns == "" || len(nonce) != 32 {
		return nil, errors.New("exact verifier attempt required")
	}
	opts := metav1.ListOptions{LabelSelector: labels.Set{verifierLabel: nonce}.AsSelector().String(), Limit: 2}
	refs := []ObjectRef{}
	continuation := ""
	switch kind {
	case "Service":
		list, err := c.k.Clientset.CoreV1().Services(ns).List(ctx, opts)
		if err != nil {
			return nil, err
		}
		continuation = list.Continue
		for _, o := range list.Items {
			refs = append(refs, ObjectRef{kind, o.Namespace, o.Name, string(o.UID)})
		}
	case "NetworkPolicy":
		list, err := c.k.Clientset.NetworkingV1().NetworkPolicies(ns).List(ctx, opts)
		if err != nil {
			return nil, err
		}
		continuation = list.Continue
		for _, o := range list.Items {
			refs = append(refs, ObjectRef{kind, o.Namespace, o.Name, string(o.UID)})
		}
	case "Pod":
		list, err := c.k.Clientset.CoreV1().Pods(ns).List(ctx, opts)
		if err != nil {
			return nil, err
		}
		continuation = list.Continue
		for _, o := range list.Items {
			refs = append(refs, ObjectRef{kind, o.Namespace, o.Name, string(o.UID)})
		}
	default:
		return nil, errors.New("unsupported verifier recovery kind")
	}
	if continuation != "" || len(refs) > 1 {
		return nil, errors.New("verifier recovery inventory is ambiguous")
	}
	return refs, nil
}
