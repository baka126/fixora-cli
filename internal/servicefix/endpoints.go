package servicefix

import (
	"errors"
	"net/netip"
	"reflect"
	"sort"

	"github.com/fixora/kubectl-fixora/internal/serviceprobe"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
)

func CheckEndpoints(svc *corev1.Service, slices []discoveryv1.EndpointSlice, s Snapshot) ([]serviceprobe.Target, error) {
	if svc == nil || svc.UID == "" || s.Service == nil || svc.Namespace != s.Service.Namespace || len(s.Pods) == 0 {
		return nil, errors.New("exact Service/backend evidence required")
	}
	selected := s.approvedPort
	if selected.Port < 1 || len(s.approvedSelector) == 0 || !reflect.DeepEqual(svc.Spec.Selector, s.approvedSelector) {
		return nil, errors.New("Service differs from approved routing")
	}
	count := 0
	for _, port := range svc.Spec.Ports {
		if port.Name == selected.Name && port.Port == selected.Port {
			if port.TargetPort != selected.TargetPort || port.Protocol != "" && port.Protocol != corev1.ProtocolTCP {
				return nil, errors.New("Service targetPort differs from approved routing")
			}
			count++
		}
	}
	if count != 1 {
		return nil, errors.New("selected Service port missing or ambiguous")
	}
	expected := map[string]serviceprobe.Target{}
	names := map[string]string{}
	families := map[bool]bool{}
	ips := svc.Spec.ClusterIPs
	if len(ips) == 0 {
		ips = []string{svc.Spec.ClusterIP}
	}
	for _, ip := range ips {
		a, err := netip.ParseAddr(ip)
		if err != nil {
			return nil, errors.New("invalid Service IP")
		}
		families[a.Is6()] = true
	}
	for _, pod := range s.Pods {
		number, err := resolvePort(pod, selected.TargetPort.IntVal, selected.TargetPort.StrVal)
		if err != nil {
			return nil, err
		}
		names[string(pod.UID)] = pod.Name
		podIPs := pod.Status.PodIPs
		if len(podIPs) == 0 {
			podIPs = []corev1.PodIP{{IP: pod.Status.PodIP}}
		}
		for _, ip := range podIPs {
			addr, err := netip.ParseAddr(ip.IP)
			if err != nil {
				return nil, errors.New("invalid backend IP")
			}
			if !families[addr.Is6()] {
				continue
			}
			expected[addr.String()] = serviceprobe.Target{Address: addr.String(), Port: int(number), PodUID: string(pod.UID)}
		}
	}
	seen := map[string]bool{}
	for _, slice := range slices {
		if err := checkSliceOwner(svc, slice); err != nil {
			return nil, err
		}
		ports := 0
		actualPort := int32(0)
		for _, p := range slice.Ports {
			name := ""
			if p.Name != nil {
				name = *p.Name
			}
			if name == selected.Name {
				if p.Port == nil || p.Protocol == nil || *p.Protocol != corev1.ProtocolTCP {
					return nil, errors.New("EndpointSlice port unavailable")
				}
				ports++
				actualPort = *p.Port
			}
		}
		if ports != 1 {
			return nil, errors.New("EndpointSlice selected port ambiguous")
		}
		for _, endpoint := range slice.Endpoints {
			ref := endpoint.TargetRef
			if endpoint.Conditions.Ready == nil || !*endpoint.Conditions.Ready || endpoint.Conditions.Terminating != nil && *endpoint.Conditions.Terminating || endpoint.Conditions.Serving != nil && !*endpoint.Conditions.Serving || ref == nil || ref.APIVersion != "v1" || ref.Kind != "Pod" || ref.Namespace != svc.Namespace || names[string(ref.UID)] != ref.Name || ref.Name == "" || len(endpoint.Addresses) == 0 {
				return nil, errors.New("unready, terminating or unexpected backend endpoint")
			}
			for _, ip := range endpoint.Addresses {
				addr, err := netip.ParseAddr(ip)
				if err != nil {
					return nil, errors.New("invalid endpoint address")
				}
				if addr.Is6() && slice.AddressType != discoveryv1.AddressTypeIPv6 || !addr.Is6() && slice.AddressType != discoveryv1.AddressTypeIPv4 {
					return nil, errors.New("EndpointSlice address family differs")
				}
				want, ok := expected[addr.String()]
				if !ok || want.PodUID != string(ref.UID) || want.Port != int(actualPort) {
					return nil, errors.New("EndpointSlice address, Pod UID or port differs")
				}
				seen[addr.String()] = true
			}
		}
	}
	if len(expected) == 0 || len(seen) != len(expected) {
		return nil, errors.New("EndpointSlices do not cover every approved backend address")
	}
	out := []serviceprobe.Target{}
	for ip, t := range expected {
		if !seen[ip] {
			return nil, errors.New("missing backend endpoint")
		}
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Address < out[j].Address })
	return out, nil
}

func checkSliceOwner(svc *corev1.Service, slice discoveryv1.EndpointSlice) error {
	if slice.Namespace != svc.Namespace || slice.UID == "" || slice.Labels["kubernetes.io/service-name"] != svc.Name || slice.Labels["endpointslice.kubernetes.io/managed-by"] != "endpointslice-controller.k8s.io" {
		return errors.New("EndpointSlice is not an exact managed Service slice")
	}
	owns := false
	for _, ref := range slice.OwnerReferences {
		if ref.Controller != nil && *ref.Controller {
			if owns || ref.APIVersion != "v1" || ref.Kind != "Service" || ref.Name != svc.Name || ref.UID != svc.UID {
				return errors.New("EndpointSlice owner differs")
			}
			owns = true
		}
	}
	if !owns {
		return errors.New("EndpointSlice controller ownership missing")
	}
	return nil
}
