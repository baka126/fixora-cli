package servicefix

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/fixora/kubectl-fixora/internal/serviceprobe"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"testing"
	"time"
)

type runtimeFixture struct {
	*fixtureReader
	svc     *corev1.Service
	pod     *corev1.Pod
	network *networkingv1.NetworkPolicy
	deleted []ObjectRef
	failure string
}

func (r *runtimeFixture) Admit(_ context.Context, c *corev1.Service) (*corev1.Service, error) {
	c = c.DeepCopy()
	if r.failure == "admission" {
		c.Spec.Selector["intruder"] = "true"
	}
	return c, nil
}
func (r *runtimeFixture) CreateService(_ context.Context, c *corev1.Service) (*corev1.Service, error) {
	r.svc = c.DeepCopy()
	r.svc.Name = "temporary"
	r.svc.UID = "temp-1"
	r.svc.ResourceVersion = "1"
	r.svc.Spec.ClusterIP = "10.0.0.2"
	r.svc.Spec.ClusterIPs = []string{"10.0.0.2"}
	if r.failure == "family-rewrite" {
		r.svc.Spec.SessionAffinity = corev1.ServiceAffinityClientIP
	}
	return r.svc.DeepCopy(), nil
}
func (r *runtimeFixture) CreatePolicy(_ context.Context, n *networkingv1.NetworkPolicy) (*networkingv1.NetworkPolicy, error) {
	r.network = n.DeepCopy()
	r.network.Name = "isolation"
	r.network.UID = "net-1"
	return r.network.DeepCopy(), nil
}
func (r *runtimeFixture) CreatePod(_ context.Context, p *corev1.Pod) (*corev1.Pod, error) {
	r.pod = p.DeepCopy()
	r.pod.Name = "verifier"
	r.pod.UID = "probe-1"
	if r.failure == "probe-rewrite" {
		r.pod.Spec.Containers[0].WorkingDir = "/other"
	}
	if r.failure == "create" {
		return r.pod.DeepCopy(), errors.New("synthetic partial creation")
	}
	return r.pod.DeepCopy(), nil
}
func (r *runtimeFixture) ReadPod(context.Context, string, string) (*corev1.Pod, error) {
	p := r.pod.DeepCopy()
	p.Status.Phase = corev1.PodSucceeded
	p.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "verifier", Image: p.Spec.Containers[0].Image, ImageID: p.Spec.Containers[0].Image, State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0}}}}
	return p, nil
}
func (r *runtimeFixture) PodLogs(context.Context, string, string, int64) ([]byte, error) {
	var req serviceprobe.Request
	if json.Unmarshal([]byte(r.pod.Spec.Containers[0].Args[1]), &req) != nil {
		return nil, errors.New("invalid probe argv")
	}
	result := serviceprobe.Result{Nonce: req.Nonce}
	for _, target := range req.Targets {
		result.Checks = append(result.Checks, serviceprobe.Check{Target: target, Success: true, Status: req.Contract.ExpectedStatus})
	}
	if r.failure == "report" {
		result.Nonce = "wrong"
	}
	if r.failure == "missing" {
		result.Checks = result.Checks[:1]
	}
	if r.failure == "duplicate" {
		result.Checks[1] = result.Checks[0]
	}
	return json.Marshal(result)
}
func (r *runtimeFixture) DeleteOwned(_ context.Context, ref ObjectRef) error {
	r.deleted = append(r.deleted, ref)
	if r.failure == "cleanup" {
		return errors.New("synthetic conflict")
	}
	return nil
}
func (r *runtimeFixture) PatchService(context.Context, string, string, []byte) (*corev1.Service, error) {
	return nil, errors.New("not used")
}
func (r *runtimeFixture) ListSlices(ctx context.Context, ns, name string) ([]discoveryv1.EndpointSlice, error) {
	if r.svc == nil || name != r.svc.Name {
		return r.fixtureReader.ListSlices(ctx, ns, name)
	}
	ready, ctrl := true, true
	portName, number, protocol := r.svc.Spec.Ports[0].Name, int32(8080), corev1.ProtocolTCP
	return []discoveryv1.EndpointSlice{{ObjectMeta: metav1.ObjectMeta{Name: "slice", Namespace: ns, UID: "slice-1", Labels: map[string]string{"kubernetes.io/service-name": name, "endpointslice.kubernetes.io/managed-by": "endpointslice-controller.k8s.io"}, OwnerReferences: []metav1.OwnerReference{{APIVersion: "v1", Kind: "Service", Name: name, UID: r.svc.UID, Controller: &ctrl}}}, AddressType: discoveryv1.AddressTypeIPv4, Ports: []discoveryv1.EndpointPort{{Name: &portName, Port: &number, Protocol: &protocol}}, Endpoints: []discoveryv1.Endpoint{{Addresses: []string{"10.0.1.1"}, Conditions: discoveryv1.EndpointConditions{Ready: &ready}, TargetRef: &corev1.ObjectReference{APIVersion: "v1", Kind: "Pod", Name: "backend-a", Namespace: ns, UID: "pod-1"}}}}}, nil
}
func TestServiceVerifyAdmittedExactCandidate(t *testing.T) {
	a, reader := serviceFixture(t)
	r := &runtimeFixture{fixtureReader: reader}
	p, err := Prepare(context.Background(), r, a, serviceFinding(), "")
	if err != nil {
		t.Fatal(err)
	}
	v, err := Verify(context.Background(), r, p)
	if err != nil || v.Receipt == nil {
		t.Fatal(v, err)
	}
	if err = v.Receipt.Check(p.Proof); err != nil {
		t.Fatal(err)
	}
	if len(r.deleted) != 3 || r.pod.Spec.AutomountServiceAccountToken == nil || *r.pod.Spec.AutomountServiceAccountToken || len(r.pod.Spec.Containers) != 1 {
		t.Fatal("sandbox cleanup/isolation", r.deleted, r.pod)
	}
}
func TestServiceSandboxIsolationAndCleanup(t *testing.T) {
	for _, bad := range []string{"admission", "create", "report", "cleanup"} {
		t.Run(bad, func(t *testing.T) {
			a, reader := serviceFixture(t)
			r := &runtimeFixture{fixtureReader: reader, failure: bad}
			p, err := Prepare(context.Background(), r, a, serviceFinding(), "")
			if err != nil {
				t.Fatal(err)
			}
			v, err := Verify(context.Background(), r, p)
			if err == nil || v.Receipt != nil {
				t.Fatal("verification forged", v)
			}
			if bad != "admission" && len(r.deleted) == 0 {
				t.Fatal("owned resources abandoned")
			}
		})
	}
}

func (r *runtimeFixture) GetService(ctx context.Context, ns, name string) (*corev1.Service, error) {
	if r.svc != nil && name == r.svc.Name {
		got := r.svc.DeepCopy()
		if r.failure == "temporary-change" {
			got.Spec.Selector = map[string]string{"wrong": "backend"}
		}
		return got, nil
	}
	return r.fixtureReader.GetService(ctx, ns, name)
}
func TestServiceVerifyTemporaryRoutingAndPublicPlanFreshness(t *testing.T) {
	for _, bad := range []string{"temporary-change", "preview-change"} {
		t.Run(bad, func(t *testing.T) {
			a, reader := serviceFixture(t)
			r := &runtimeFixture{fixtureReader: reader, failure: bad}
			p, err := Prepare(context.Background(), r, a, serviceFinding(), "")
			if err != nil {
				t.Fatal(err)
			}
			if bad == "preview-change" {
				p.Diff = "operator saw a different change"
			}
			v, err := Verify(context.Background(), r, p)
			if err == nil || v.Receipt != nil {
				t.Fatal("stale routing or preview certified")
			}
		})
	}
}

func TestServiceVerifyEveryEndpointAndServiceIP(t *testing.T) {
	for _, bad := range []string{"missing", "duplicate", "probe-rewrite", "family-rewrite"} {
		t.Run(bad, func(t *testing.T) {
			a, reader := serviceFixture(t)
			r := &runtimeFixture{fixtureReader: reader, failure: bad}
			plan, err := Prepare(context.Background(), r, a, serviceFinding(), "")
			if err != nil {
				t.Fatal(err)
			}
			v, err := Verify(context.Background(), r, plan)
			if err == nil || v.Receipt != nil {
				t.Fatal("unsafe verification", bad, v)
			}
		})
	}
}

func TestServiceSandboxUsesCallerDeadline(t *testing.T) {
	a, reader := serviceFixture(t)
	r := &runtimeFixture{fixtureReader: reader}
	plan, err := Prepare(context.Background(), r, a, serviceFinding(), "")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	_, err = Verify(ctx, r, plan)
	if err != nil {
		t.Fatal(err)
	}
	if *r.pod.Spec.ActiveDeadlineSeconds < 590 {
		t.Fatal("caller deadline silently capped", *r.pod.Spec.ActiveDeadlineSeconds)
	}
}
