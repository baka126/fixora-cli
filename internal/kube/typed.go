package kube

import (
	"bytes"
	"context"
	stdjson "encoding/json"
	"fmt"
	"io"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
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
	cfg, err := typedRESTConfig(contextName)
	if err != nil {
		return nil, err
	}
	clientset, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	dynamicClient, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	discoveryClient, err := discovery.NewDiscoveryClientForConfig(cfg)
	if err != nil {
		return nil, err
	}
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		return nil, err
	}
	runtimeClient, err := ctrlclient.New(cfg, ctrlclient.Options{Scheme: scheme})
	if err != nil {
		return nil, err
	}
	mapper := restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(discoveryClient))
	fallback := NewKubectl(contextName)
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
	if err == nil {
		return cfg, nil
	}
	inCluster, inClusterErr := rest.InClusterConfig()
	if inClusterErr == nil {
		return inCluster, nil
	}
	return nil, err
}

func (c *TypedClient) Status(ctx context.Context) (Status, error) {
	version, err := c.Discovery.ServerVersion()
	if err != nil {
		return Status{}, err
	}
	return Status{KubectlAvailable: true, Context: c.Context, ServerVersion: version.String()}, nil
}

func (c *TypedClient) GetPods(ctx context.Context, namespace string, allNS bool) (PodList, error) {
	listOpts := metav1.ListOptions{}
	ns := namespaceForTyped(namespace, allNS)
	pods, err := c.Clientset.CoreV1().Pods(ns).List(ctx, listOpts)
	if err != nil {
		return PodList{}, err
	}
	var out PodList
	return out, convertTyped(pods, &out)
}

func (c *TypedClient) GetPod(ctx context.Context, namespace, name string) (Pod, error) {
	pod, err := c.Clientset.CoreV1().Pods(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return Pod{}, err
	}
	var out Pod
	return out, convertTyped(pod, &out)
}

func (c *TypedClient) GetEvents(ctx context.Context, namespace string) ([]Event, error) {
	events, err := c.Clientset.CoreV1().Events(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	var out EventList
	return out.Items, convertTyped(events, &out)
}

func (c *TypedClient) GetNodes(ctx context.Context) ([]Node, error) {
	nodes, err := c.Clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	var out NodeList
	return out.Items, convertTyped(nodes, &out)
}

func (c *TypedClient) GetResource(ctx context.Context, namespace, resource string) (map[string]any, error) {
	gvr, name, err := c.resourceRef(resource)
	if err != nil {
		return nil, err
	}
	obj, err := c.dynamicResource(gvr, namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	return obj.Object, nil
}

func (c *TypedClient) GetResourceItems(ctx context.Context, namespace string, allNS bool, resource string) ([]map[string]any, error) {
	gvr, _, err := c.resourceRef(resource)
	if err != nil {
		return nil, err
	}
	list, err := c.dynamicResource(gvr, namespaceForTyped(namespace, allNS)).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	items := make([]map[string]any, 0, len(list.Items))
	for _, item := range list.Items {
		items = append(items, item.Object)
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
