package kube

import (
	"bytes"
	"context"
	stdjson "encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
	"k8s.io/client-go/tools/clientcmd"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"
)

type TypedClient struct {
	Context       string
	LogTail       int
	LogLimitBytes int

	Config        *rest.Config
	Clientset     kubernetes.Interface
	Dynamic       dynamic.Interface
	RuntimeClient ctrlclient.Client
	Discovery     discovery.DiscoveryInterface
	Mapper        *restmapper.DeferredDiscoveryRESTMapper
	Fallback      Kubectl
}

func NewTypedClient(contextName string) (*TypedClient, error) {
	fallback := NewKubectl(contextName)
	cfg, err := typedRESTConfig(contextName)
	if err != nil {
		return &TypedClient{Context: contextName, Fallback: fallback, LogTail: fallback.LogTail, LogLimitBytes: fallback.LogLimitBytes}, nil
	}
	clientset, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return &TypedClient{Context: contextName, Fallback: fallback, LogTail: fallback.LogTail, LogLimitBytes: fallback.LogLimitBytes}, nil
	}
	dynamicClient, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return &TypedClient{Context: contextName, Fallback: fallback, LogTail: fallback.LogTail, LogLimitBytes: fallback.LogLimitBytes}, nil
	}
	discoveryClient, err := discovery.NewDiscoveryClientForConfig(cfg)
	if err != nil {
		return &TypedClient{Context: contextName, Fallback: fallback, LogTail: fallback.LogTail, LogLimitBytes: fallback.LogLimitBytes}, nil
	}
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		return &TypedClient{Context: contextName, Fallback: fallback, LogTail: fallback.LogTail, LogLimitBytes: fallback.LogLimitBytes}, nil
	}
	runtimeClient, err := ctrlclient.New(cfg, ctrlclient.Options{Scheme: scheme})
	if err != nil {
		return &TypedClient{Context: contextName, Fallback: fallback, LogTail: fallback.LogTail, LogLimitBytes: fallback.LogLimitBytes}, nil
	}
	mapper := restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(discoveryClient))
	return &TypedClient{
		Context:       contextName,
		LogTail:       fallback.LogTail,
		LogLimitBytes: fallback.LogLimitBytes,
		Config:        cfg,
		Clientset:     clientset,
		Dynamic:       dynamicClient,
		RuntimeClient: runtimeClient,
		Discovery:     discoveryClient,
		Mapper:        mapper,
		Fallback:      fallback,
	}, nil
}

func typedRESTConfig(contextName string) (*rest.Config, error) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	overrides := &clientcmd.ConfigOverrides{CurrentContext: contextName}
	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, overrides).ClientConfig()
	if err != nil {
		inCluster, inClusterErr := rest.InClusterConfig()
		if inClusterErr == nil {
			cfg = inCluster
		} else {
			return nil, err
		}
	}
	cfg.QPS = 50
	cfg.Burst = 100
	return cfg, nil
}

func (c *TypedClient) Status(ctx context.Context) (Status, error) {
	if c.Discovery == nil {
		return c.Fallback.Status(ctx)
	}
	version, err := c.Discovery.ServerVersion()
	if err != nil {
		return Status{}, err
	}
	return Status{KubectlAvailable: true, Context: c.Context, ServerVersion: version.String()}, nil
}

func (c *TypedClient) GetPods(ctx context.Context, namespace string, allNS bool) (PodList, error) {
	if c.Clientset == nil {
		return c.Fallback.GetPods(ctx, namespace, allNS)
	}
	ns := namespaceForTyped(namespace, allNS)
	var allItems []corev1.Pod
	var continueToken string
	for {
		listOpts := metav1.ListOptions{Limit: 500, Continue: continueToken}
		pods, err := withRetry(func() (*corev1.PodList, error) {
			return c.Clientset.CoreV1().Pods(ns).List(ctx, listOpts)
		})
		if err != nil {
			return PodList{}, err
		}
		allItems = append(allItems, pods.Items...)
		continueToken = pods.Continue
		if continueToken == "" {
			break
		}
	}
	var out PodList
	return out, convertTyped(&corev1.PodList{Items: allItems}, &out)
}

func (c *TypedClient) GetPod(ctx context.Context, namespace, name string) (Pod, error) {
	if c.Clientset == nil {
		return c.Fallback.GetPod(ctx, namespace, name)
	}
	pod, err := withRetry(func() (*corev1.Pod, error) {
		return c.Clientset.CoreV1().Pods(namespace).Get(ctx, name, metav1.GetOptions{})
	})
	if err != nil {
		return Pod{}, err
	}
	var out Pod
	return out, convertTyped(pod, &out)
}

func (c *TypedClient) GetTypedPod(ctx context.Context, namespace, name string) (*corev1.Pod, error) {
	if c.Clientset == nil {
		return nil, fmt.Errorf("typed Kubernetes client is not configured")
	}
	return withRetry(func() (*corev1.Pod, error) {
		return c.Clientset.CoreV1().Pods(namespace).Get(ctx, name, metav1.GetOptions{})
	})
}

func (c *TypedClient) CreatePod(ctx context.Context, pod *corev1.Pod) (*corev1.Pod, error) {
	if c.Clientset == nil {
		return nil, fmt.Errorf("typed Kubernetes client is not configured")
	}
	return withRetry(func() (*corev1.Pod, error) {
		return c.Clientset.CoreV1().Pods(pod.Namespace).Create(ctx, pod, metav1.CreateOptions{})
	})
}

func (c *TypedClient) DeletePod(ctx context.Context, namespace, name string) error {
	if c.Clientset == nil {
		return fmt.Errorf("typed Kubernetes client is not configured")
	}
	return c.Clientset.CoreV1().Pods(namespace).Delete(ctx, name, metav1.DeleteOptions{})
}

func (c *TypedClient) WatchPod(ctx context.Context, namespace, name string) (watch.Interface, error) {
	if c.Clientset == nil {
		return nil, fmt.Errorf("typed Kubernetes client is not configured")
	}
	return c.Clientset.CoreV1().Pods(namespace).Watch(ctx, metav1.ListOptions{
		FieldSelector: "metadata.name=" + name,
	})
}

func (c *TypedClient) CreateNetworkPolicy(ctx context.Context, policy *networkingv1.NetworkPolicy) (*networkingv1.NetworkPolicy, error) {
	if c.Clientset == nil {
		return nil, fmt.Errorf("typed Kubernetes client is not configured")
	}
	return withRetry(func() (*networkingv1.NetworkPolicy, error) {
		return c.Clientset.NetworkingV1().NetworkPolicies(policy.Namespace).Create(ctx, policy, metav1.CreateOptions{})
	})
}

func (c *TypedClient) DeleteNetworkPolicy(ctx context.Context, namespace, name string) error {
	if c.Clientset == nil {
		return fmt.Errorf("typed Kubernetes client is not configured")
	}
	return c.Clientset.NetworkingV1().NetworkPolicies(namespace).Delete(ctx, name, metav1.DeleteOptions{})
}

func (c *TypedClient) GetEvents(ctx context.Context, namespace string) ([]Event, error) {
	if c.Clientset == nil {
		return c.Fallback.GetEvents(ctx, namespace)
	}
	var allItems []corev1.Event
	var continueToken string
	for {
		listOpts := metav1.ListOptions{Limit: 500, Continue: continueToken}
		events, err := withRetry(func() (*corev1.EventList, error) {
			return c.Clientset.CoreV1().Events(namespace).List(ctx, listOpts)
		})
		if err != nil {
			return nil, err
		}
		allItems = append(allItems, events.Items...)
		continueToken = events.Continue
		if continueToken == "" {
			break
		}
	}
	var out EventList
	return out.Items, convertTyped(&corev1.EventList{Items: allItems}, &out)
}

func (c *TypedClient) GetNodes(ctx context.Context) ([]Node, error) {
	if c.Clientset == nil {
		return c.Fallback.GetNodes(ctx)
	}
	var allItems []corev1.Node
	var continueToken string
	for {
		listOpts := metav1.ListOptions{Limit: 500, Continue: continueToken}
		nodes, err := withRetry(func() (*corev1.NodeList, error) {
			return c.Clientset.CoreV1().Nodes().List(ctx, listOpts)
		})
		if err != nil {
			return nil, err
		}
		allItems = append(allItems, nodes.Items...)
		continueToken = nodes.Continue
		if continueToken == "" {
			break
		}
	}
	var out NodeList
	return out.Items, convertTyped(&corev1.NodeList{Items: allItems}, &out)
}

func (c *TypedClient) GetResource(ctx context.Context, namespace, resource string) (map[string]any, error) {
	if c.Mapper == nil || c.Dynamic == nil {
		return c.Fallback.GetResource(ctx, namespace, resource)
	}
	gvr, name, err := c.resourceRef(resource)
	if err != nil {
		return nil, err
	}
	obj, err := withRetry(func() (*unstructured.Unstructured, error) {
		res := c.dynamicResource(gvr, namespace)
		return res.Get(ctx, name, metav1.GetOptions{})
	})
	if err != nil {
		return nil, err
	}
	return obj.Object, nil
}

func (c *TypedClient) GetResourceItems(ctx context.Context, namespace string, allNS bool, resource string) ([]map[string]any, error) {
	if c.Mapper == nil || c.Dynamic == nil {
		return c.Fallback.GetResourceItems(ctx, namespace, allNS, resource)
	}
	gvr, _, err := c.resourceRef(resource)
	if err != nil {
		return nil, err
	}
	var items []map[string]any
	var continueToken string
	for {
		listOpts := metav1.ListOptions{Limit: 500, Continue: continueToken}
		list, err := withRetry(func() (*unstructured.UnstructuredList, error) {
			res := c.dynamicResource(gvr, namespaceForTyped(namespace, allNS))
			return res.List(ctx, listOpts)
		})
		if err != nil {
			return nil, err
		}
		for _, item := range list.Items {
			items = append(items, item.Object)
		}
		continueToken = list.GetContinue()
		if continueToken == "" {
			break
		}
	}
	return items, nil
}

func (c *TypedClient) Logs(ctx context.Context, namespace, pod string, previous bool) (string, error) {
	tail := int64(c.LogTail)
	if tail <= 0 {
		tail = 120
	}
	limitBytes := int64(c.LogLimitBytes)
	if limitBytes <= 0 {
		limitBytes = 24000
	}
	req := c.Clientset.CoreV1().Pods(namespace).GetLogs(pod, &corev1.PodLogOptions{
		Previous:   previous,
		TailLines:  &tail,
		LimitBytes: &limitBytes,
	})
	stream, err := req.Stream(ctx)
	if err != nil {
		return "", err
	}
	defer stream.Close()
	var b bytes.Buffer
	_, err = io.Copy(&b, stream)
	return b.String(), err
}

func (c *TypedClient) Run(ctx context.Context, args ...string) ([]byte, error) {
	return c.Fallback.Run(ctx, args...)
}

func (c *TypedClient) resourceRef(resource string) (schema.GroupVersionResource, string, error) {
	kind, name := splitResourceName(resource)
	candidates := []schema.GroupVersionResource{resourceCandidate(kind)}
	for _, candidate := range candidates {
		mapped, err := c.Mapper.ResourceFor(candidate)
		if err == nil {
			return mapped, name, nil
		}
	}
	return schema.GroupVersionResource{}, "", fmt.Errorf("resource %q not found by discovery", resource)
}

func (c *TypedClient) dynamicResource(gvr schema.GroupVersionResource, namespace string) dynamic.ResourceInterface {
	if namespace == "" {
		return c.Dynamic.Resource(gvr)
	}
	return c.Dynamic.Resource(gvr).Namespace(namespace)
}

func resourceCandidate(resource string) schema.GroupVersionResource {
	resource = strings.ToLower(strings.TrimSpace(resource))
	switch resource {
	case "hpa":
		return schema.GroupVersionResource{Group: "autoscaling", Version: "v2", Resource: "horizontalpodautoscalers"}
	case "pdb":
		return schema.GroupVersionResource{Group: "policy", Version: "v1", Resource: "poddisruptionbudgets"}
	}
	parts := strings.Split(resource, ".")
	if len(parts) > 1 {
		return schema.GroupVersionResource{Group: strings.Join(parts[1:], "."), Resource: parts[0]}
	}
	return schema.GroupVersionResource{Resource: resource}
}

func splitResourceName(resource string) (string, string) {
	parts := strings.SplitN(resource, "/", 2)
	if len(parts) == 2 {
		return parts[0], parts[1]
	}
	return resource, ""
}

func namespaceForTyped(namespace string, allNS bool) string {
	if allNS {
		return ""
	}
	return namespace
}

func convertTyped(source, target any) error {
	data, err := stdjson.Marshal(source)
	if err != nil {
		return err
	}
	return stdjson.Unmarshal(data, target)
}

func withRetry[T any](f func() (T, error)) (T, error) {
	var err error
	var zero T
	for i := 0; i < 3; i++ {
		res, reqErr := f()
		if reqErr == nil {
			return res, nil
		}
		err = reqErr
		time.Sleep(time.Duration(1<<i) * 100 * time.Millisecond)
	}
	return zero, err
}
