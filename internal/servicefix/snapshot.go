package servicefix

import (
	"context"
	"encoding/json"
	"errors"
	"net/netip"
	"strings"

	"github.com/fixora/kubectl-fixora/internal/kube"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
)

type Reader interface {
	GetNamespace(context.Context, string) (*corev1.Namespace, error)
	GetService(context.Context, string, string) (*corev1.Service, error)
	GetWorkload(context.Context, string, string, string) (map[string]any, error)
	ListPods(context.Context, string) ([]corev1.Pod, error)
	ListSlices(context.Context, string, string) ([]discoveryv1.EndpointSlice, error)
	ResolveOwner(context.Context, corev1.Pod) (kube.WorkloadOwner, error)
}
type Snapshot struct {
	approvedPort     corev1.ServicePort
	approvedSelector map[string]string
	Namespace        *corev1.Namespace             `json:"-" yaml:"-"`
	Service          *corev1.Service               `json:"-" yaml:"-"`
	Backend          map[string]any                `json:"-" yaml:"-"`
	Pods             []corev1.Pod                  `json:"-" yaml:"-"`
	Slices           []discoveryv1.EndpointSlice   `json:"-" yaml:"-"`
	Owners           map[string]kube.WorkloadOwner `json:"-" yaml:"-"`
}

func Collect(ctx context.Context, r Reader, a *Authority) (Snapshot, error) {
	s := Snapshot{Owners: map[string]kube.WorkloadOwner{}}
	if err := ctx.Err(); err != nil {
		return s, err
	}
	if r == nil {
		return s, errors.New("Service cluster reader required")
	}
	if err := a.check(); err != nil {
		return s, err
	}
	p := a.intent
	s.approvedPort = corev1.ServicePort{Name: p.Spec.Port.Name, Port: p.Spec.Port.Port, TargetPort: p.Spec.Port.TargetPort, Protocol: corev1.ProtocolTCP}
	s.approvedSelector = map[string]string{}
	for k, v := range p.Spec.Selector {
		s.approvedSelector[k] = v
	}
	ns := p.Metadata.Namespace
	namespace, err := r.GetNamespace(ctx, ns)
	if err != nil || namespace == nil || namespace.Name != ns || namespace.UID == "" || namespace.DeletionTimestamp != nil || namespace.Status.Phase == corev1.NamespaceTerminating {
		return s, errors.New("namespace identity unavailable")
	}
	s.Namespace = namespace.DeepCopy()
	svc, err := r.GetService(ctx, ns, p.Spec.Service.Name)
	if err != nil || svc == nil || svc.Name != p.Spec.Service.Name || svc.Namespace != ns || svc.UID == "" || svc.ResourceVersion == "" {
		return s, errors.New("Service identity unavailable")
	}
	if err = validateService(svc, a); err != nil {
		return s, err
	}
	s.Service = svc.DeepCopy()
	backend, err := r.GetWorkload(ctx, ns, p.Spec.Backend.Kind, p.Spec.Backend.Name)
	if err != nil {
		return s, errors.New("approved backend unavailable")
	}
	meta, _ := backend["metadata"].(map[string]any)
	uid, _ := meta["uid"].(string)
	if uid == "" || backend["apiVersion"] != "apps/v1" || backend["kind"] != p.Spec.Backend.Kind || meta["name"] != p.Spec.Backend.Name || meta["namespace"] != ns || meta["deletionTimestamp"] != nil {
		return s, errors.New("backend identity differs from policy")
	}
	raw, _ := json.Marshal(backend)
	if json.Unmarshal(raw, &s.Backend) != nil {
		return s, errors.New("backend snapshot invalid")
	}
	pods, err := r.ListPods(ctx, ns)
	if err != nil {
		return s, errors.New("backend Pod inventory unreadable")
	}
	addresses := map[string]bool{}
	uids := map[string]bool{}
	resolvedPort := int32(0)
	for _, pod := range pods {
		if pod.Namespace != ns || !matches(p.Spec.Selector, pod.Labels) {
			continue
		}
		if len(s.Pods) >= maxBackends || pod.UID == "" || uids[string(pod.UID)] {
			return s, errors.New("backend identity missing, duplicate or over limit")
		}
		owner, err := r.ResolveOwner(ctx, pod)
		if err != nil || owner.Kind != p.Spec.Backend.Kind || owner.Name != p.Spec.Backend.Name || owner.UID != uid {
			return s, errors.New("selector includes unrelated or unverified backend")
		}
		if pod.DeletionTimestamp != nil || !podReady(pod) {
			return s, errors.New("selected backend is not Ready or is terminating")
		}
		port, portErr := resolvePort(pod, p.Spec.Port.TargetPort.IntVal, p.Spec.Port.TargetPort.StrVal)
		if portErr != nil {
			err = portErr
		}
		if err != nil {
			return s, err
		}
		if resolvedPort != 0 && port != resolvedPort {
			return s, errors.New("approved named targetPort differs across backends")
		}
		resolvedPort = port
		ips := pod.Status.PodIPs
		if len(ips) == 0 && pod.Status.PodIP != "" {
			ips = []corev1.PodIP{{IP: pod.Status.PodIP}}
		}
		if len(ips) == 0 {
			return s, errors.New("backend has no IP address")
		}
		for _, ip := range ips {
			addr, err := netip.ParseAddr(ip.IP)
			if err != nil || addr.IsUnspecified() || addr.IsLoopback() || addr.IsMulticast() || addr.Zone() != "" || addresses[addr.String()] {
				return s, errors.New("backend IP missing, invalid or shared")
			}
			addresses[addr.String()] = true
		}
		if len(addresses) > maxAddresses {
			return s, errors.New("backend address limit exceeded")
		}
		uids[string(pod.UID)] = true
		s.Pods = append(s.Pods, *pod.DeepCopy())
		s.Owners[string(pod.UID)] = owner
	}
	if len(s.Pods) == 0 {
		return s, errors.New("approved selector has no Ready owned backends")
	}
	slices, err := r.ListSlices(ctx, ns, svc.Name)
	if err != nil {
		return s, errors.New("EndpointSlice inventory unreadable")
	}
	for _, slice := range slices {
		if err := checkSliceOwner(svc, slice); err != nil {
			return s, err
		}
		s.Slices = append(s.Slices, *slice.DeepCopy())
	}
	if err = a.check(); err != nil {
		return s, err
	}
	return s, ctx.Err()
}
func validateService(s *corev1.Service, a *Authority) error {
	if s.DeletionTimestamp != nil || (s.Spec.Type != "" && s.Spec.Type != corev1.ServiceTypeClusterIP) || s.Spec.ClusterIP == "" || s.Spec.ClusterIP == "None" || len(s.Spec.Selector) == 0 || s.Spec.PublishNotReadyAddresses || s.Spec.ExternalName != "" || len(s.Spec.ExternalIPs) > 0 {
		return errors.New("unsupported Service routing; source review required")
	}
	if s.Labels["service.kubernetes.io/headless"] != "" {
		return errors.New("headless routing requires review")
	}
	for k := range s.Annotations {
		if strings.Contains(k, "istio.io") || strings.Contains(k, "linkerd.io") {
			return errors.New("service mesh routing requires review")
		}
	}
	ips := s.Spec.ClusterIPs
	if len(ips) == 0 {
		ips = []string{s.Spec.ClusterIP}
	}
	if len(ips) > 2 || ips[0] != s.Spec.ClusterIP {
		return errors.New("invalid Service addresses")
	}
	seen := map[string]bool{}
	for _, ip := range ips {
		a, err := netip.ParseAddr(ip)
		if err != nil || a.IsLoopback() || a.IsUnspecified() || a.IsMulticast() || a.Zone() != "" || seen[a.String()] {
			return errors.New("invalid Service address")
		}
		seen[a.String()] = true
	}
	count := 0
	for _, port := range s.Spec.Ports {
		if port.Protocol != "" && port.Protocol != corev1.ProtocolTCP {
			return errors.New("only TCP Service ports are supported")
		}
		if port.Port == a.intent.Spec.Port.Port && port.Name == a.intent.Spec.Port.Name {
			count++
		}
	}
	if count != 1 {
		return errors.New("selected Service port is missing or ambiguous")
	}
	return nil
}
func matches(selector, labels map[string]string) bool {
	for k, v := range selector {
		if actual, ok := labels[k]; !ok || actual != v {
			return false
		}
	}
	return true
}
func podReady(p corev1.Pod) bool {
	for _, c := range p.Status.Conditions {
		if c.Type == corev1.PodReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}
func resolvePort(p corev1.Pod, number int32, name string) (int32, error) {
	found := int32(0)
	for _, c := range p.Spec.Containers {
		for _, port := range c.Ports {
			if port.Protocol != "" && port.Protocol != corev1.ProtocolTCP {
				continue
			}
			if name != "" && port.Name != name || name == "" && port.ContainerPort != number {
				continue
			}
			if found != 0 && found != port.ContainerPort {
				return 0, errors.New("backend targetPort resolves ambiguously")
			}
			found = port.ContainerPort
		}
	}
	if found < 1 || found > 65535 {
		return 0, errors.New("approved targetPort is not exposed by a regular backend container")
	}
	return found, nil
}
