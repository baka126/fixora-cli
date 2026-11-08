package servicefix

import (
	"context"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"testing"
)

func endpointFixture(t *testing.T) (*corev1.Service, []discoveryv1.EndpointSlice, Snapshot) {
	a, r := serviceFixture(t)
	s, err := Collect(context.Background(), r, a)
	if err != nil {
		t.Fatal(err)
	}
	svc := r.service.DeepCopy()
	svc.Name = "temporary"
	svc.UID = "temp-1"
	svc.Spec.Selector = map[string]string{"workload": "arbitrary"}
	svc.Spec.Ports[0].TargetPort = intstr.FromString("http")
	ready, controller := true, true
	name, protocol, port := "http", corev1.ProtocolTCP, int32(8080)
	return svc, []discoveryv1.EndpointSlice{{ObjectMeta: metav1.ObjectMeta{Name: "slice", Namespace: "sample", UID: "slice-1", Labels: map[string]string{"kubernetes.io/service-name": "temporary", "endpointslice.kubernetes.io/managed-by": "endpointslice-controller.k8s.io"}, OwnerReferences: []metav1.OwnerReference{{APIVersion: "v1", Kind: "Service", Name: "temporary", UID: "temp-1", Controller: &controller}}}, AddressType: discoveryv1.AddressTypeIPv4, Ports: []discoveryv1.EndpointPort{{Name: &name, Protocol: &protocol, Port: &port}}, Endpoints: []discoveryv1.Endpoint{{Addresses: []string{"10.0.1.1"}, Conditions: discoveryv1.EndpointConditions{Ready: &ready}, TargetRef: &corev1.ObjectReference{APIVersion: "v1", Kind: "Pod", Namespace: "sample", Name: "backend-a", UID: "pod-1"}}}}}, s
}
func TestServiceEndpointIdentityAndReadiness(t *testing.T) {
	svc, slices, s := endpointFixture(t)
	targets, err := CheckEndpoints(svc, slices, s)
	if err != nil || len(targets) != 1 {
		t.Fatal(targets, err)
	}
	for _, bad := range []string{"UID", "manager", "ready", "port", "address"} {
		t.Run(bad, func(t *testing.T) {
			svc, slices, s := endpointFixture(t)
			switch bad {
			case "UID":
				slices[0].OwnerReferences[0].UID = "other"
			case "manager":
				slices[0].Labels["endpointslice.kubernetes.io/managed-by"] = "manual"
			case "ready":
				slices[0].Endpoints[0].Conditions.Ready = nil
			case "port":
				n := int32(9090)
				slices[0].Ports[0].Port = &n
			case "address":
				slices[0].Endpoints[0].Addresses = []string{"10.0.9.9"}
			}
			if _, err := CheckEndpoints(svc, slices, s); err == nil {
				t.Fatal("unproved endpoint")
			}
		})
	}
}

func TestServiceEndpointSelectedPortIsNotFirstOrBackendName(t *testing.T) {
	a, r := serviceFixture(t)
	a.intent.Spec.Port.Name = "frontend"
	r.service.Spec.Ports[0].Name = "frontend"
	r.service.Spec.Ports = append([]corev1.ServicePort{{Name: "metrics", Port: 90, TargetPort: intstr.FromInt(9090), Protocol: corev1.ProtocolTCP}}, r.service.Spec.Ports...)
	s, err := Collect(context.Background(), r, a)
	if err != nil {
		t.Fatal(err)
	}
	svc, slices, _ := endpointFixture(t)
	svc.Spec.Ports = r.service.DeepCopy().Spec.Ports
	svc.Spec.Ports[1].TargetPort = intstr.FromString("http")
	svc.Spec.Selector = map[string]string{"workload": "arbitrary"}
	n := "frontend"
	slices[0].Ports[0].Name = &n
	if _, err := CheckEndpoints(svc, slices, s); err != nil {
		t.Fatal("wrong selected port resolution", err)
	}
}
