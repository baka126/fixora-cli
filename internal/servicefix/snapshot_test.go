package servicefix

import (
	"context"
	"errors"
	"github.com/fixora/kubectl-fixora/internal/kube"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"testing"
)

type fixtureReader struct {
	service *corev1.Service
	backend map[string]any
	pods    []corev1.Pod
	failure bool
	owner   kube.WorkloadOwner
}

func (r *fixtureReader) GetNamespace(context.Context, string) (*corev1.Namespace, error) {
	return &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "sample", UID: "ns-1"}}, nil
}
func (r *fixtureReader) GetService(context.Context, string, string) (*corev1.Service, error) {
	if r.failure {
		return nil, errors.New("synthetic denial")
	}
	return r.service.DeepCopy(), nil
}
func (r *fixtureReader) GetWorkload(context.Context, string, string, string) (map[string]any, error) {
	return r.backend, nil
}
func (r *fixtureReader) ListPods(context.Context, string) ([]corev1.Pod, error) { return r.pods, nil }
func (r *fixtureReader) ListSlices(context.Context, string, string) ([]discoveryv1.EndpointSlice, error) {
	if r.failure {
		return nil, errors.New("synthetic denial")
	}
	return nil, nil
}
func (r *fixtureReader) ResolveOwner(context.Context, corev1.Pod) (kube.WorkloadOwner, error) {
	return r.owner, nil
}
func serviceFixture(t *testing.T) (*Authority, *fixtureReader) {
	t.Helper()
	a, err := LoadPolicy(policyFile(t, validPolicy))
	if err != nil {
		t.Fatal(err)
	}
	return a, &fixtureReader{service: &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "gateway", Namespace: "sample", UID: "svc-1", ResourceVersion: "1"}, Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeClusterIP, ClusterIP: "10.0.0.1", ClusterIPs: []string{"10.0.0.1"}, Selector: map[string]string{"workload": "wrong"}, Ports: []corev1.ServicePort{{Name: "http", Port: 80, TargetPort: intstr.FromInt(9090), Protocol: corev1.ProtocolTCP}}}}, backend: map[string]any{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": map[string]any{"name": "arbitrary-app", "namespace": "sample", "uid": "owner-1"}}, owner: kube.WorkloadOwner{Kind: "Deployment", Name: "arbitrary-app", UID: "owner-1"}, pods: []corev1.Pod{{ObjectMeta: metav1.ObjectMeta{Name: "backend-a", Namespace: "sample", UID: types.UID("pod-1"), Labels: map[string]string{"workload": "arbitrary"}}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Ports: []corev1.ContainerPort{{Name: "http", ContainerPort: 8080, Protocol: corev1.ProtocolTCP}}}}}, Status: corev1.PodStatus{PodIP: "10.0.1.1", PodIPs: []corev1.PodIP{{IP: "10.0.1.1"}}, Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}}}}}
}
func TestServiceSnapshotExactOwnedMembership(t *testing.T) {
	a, r := serviceFixture(t)
	s, err := Collect(context.Background(), r, a)
	if err != nil || len(s.Pods) != 1 {
		t.Fatal(s, err)
	}
	for _, bad := range []string{"owner", "ready", "namespace", "terminating", "type", "headless", "port"} {
		t.Run(bad, func(t *testing.T) {
			a, r := serviceFixture(t)
			switch bad {
			case "owner":
				r.owner.UID = "other"
			case "ready":
				r.pods[0].Status.Conditions = nil
			case "namespace":
				r.service.Namespace = "other"
			case "terminating":
				n := metav1.Now()
				r.pods[0].DeletionTimestamp = &n
			case "type":
				r.service.Spec.Type = corev1.ServiceTypeNodePort
			case "headless":
				r.service.Spec.ClusterIP = "None"
			case "port":
				r.pods[0].Spec.Containers[0].Ports[0].Name = "metrics"
			}
			if _, err := Collect(context.Background(), r, a); err == nil {
				t.Fatal("unsafe snapshot")
			}
		})
	}
}
func TestServiceSnapshotReadFailures(t *testing.T) {
	a, r := serviceFixture(t)
	r.failure = true
	if _, err := Collect(context.Background(), r, a); err == nil {
		t.Fatal("unreadable")
	}
	a, r = serviceFixture(t)
	r.pods = nil
	if _, err := Collect(context.Background(), r, a); err == nil {
		t.Fatal("empty")
	}
}

func TestServiceSnapshotNamedPortConsistency(t *testing.T) {
	a, r := serviceFixture(t)
	second := *r.pods[0].DeepCopy()
	second.Name = "backend-b"
	second.UID = "pod-2"
	second.Status.PodIP = "10.0.1.2"
	second.Status.PodIPs = []corev1.PodIP{{IP: "10.0.1.2"}}
	second.Spec.Containers[0].Ports[0].ContainerPort = 8081
	r.pods = append(r.pods, second)
	if _, err := Collect(context.Background(), r, a); err == nil {
		t.Fatal("inconsistent named targetPort accepted")
	}
}
