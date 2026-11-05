package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fixora/kubectl-fixora/internal/analyzer"
	"github.com/fixora/kubectl-fixora/internal/kube"
	"github.com/fixora/kubectl-fixora/internal/servicefix"
	"github.com/fixora/kubectl-fixora/internal/serviceprobe"
	jsonpatch "gopkg.in/evanphx/json-patch.v4"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

type serviceCLIStub struct {
	svc, temp                 *corev1.Service
	pod                       *corev1.Pod
	network                   *networkingv1.NetworkPolicy
	creates, patches, deletes int
	badLive                   bool
}

func (r *serviceCLIStub) GetNamespace(context.Context, string) (*corev1.Namespace, error) {
	return &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "team", UID: "ns"}}, nil
}
func (r *serviceCLIStub) GetService(_ context.Context, _, name string) (*corev1.Service, error) {
	if r.temp != nil && name == r.temp.Name {
		return r.temp.DeepCopy(), nil
	}
	return r.svc.DeepCopy(), nil
}
func (r *serviceCLIStub) GetWorkload(context.Context, string, string, string) (map[string]any, error) {
	return map[string]any{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": map[string]any{"name": "unrelated-api", "namespace": "team", "uid": "backend"}}, nil
}
func (r *serviceCLIStub) ListPods(context.Context, string) ([]corev1.Pod, error) {
	return []corev1.Pod{{ObjectMeta: metav1.ObjectMeta{Name: "replica", Namespace: "team", UID: "pod", Labels: map[string]string{"component": "api"}}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "worker", Ports: []corev1.ContainerPort{{Name: "web", ContainerPort: 8080}}}}}, Status: corev1.PodStatus{PodIP: "10.1.0.2", Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}}}}, nil
}
func (r *serviceCLIStub) ResolveOwner(context.Context, corev1.Pod) (kube.WorkloadOwner, error) {
	return kube.WorkloadOwner{Kind: "Deployment", Name: "unrelated-api", UID: "backend"}, nil
}
func (r *serviceCLIStub) ListSlices(_ context.Context, _, name string) ([]discoveryv1.EndpointSlice, error) {
	svc := r.svc
	if r.temp != nil && name == r.temp.Name {
		svc = r.temp
	}
	if svc.Spec.Selector["component"] != "api" {
		return nil, nil
	}
	ready, control := true, true
	portName, number, protocol := "public", int32(8080), corev1.ProtocolTCP
	return []discoveryv1.EndpointSlice{{ObjectMeta: metav1.ObjectMeta{Name: "slice-" + name, Namespace: "team", UID: svc.UID, Labels: map[string]string{"kubernetes.io/service-name": name, "endpointslice.kubernetes.io/managed-by": "endpointslice-controller.k8s.io"}, OwnerReferences: []metav1.OwnerReference{{APIVersion: "v1", Kind: "Service", Name: name, UID: svc.UID, Controller: &control}}}, AddressType: discoveryv1.AddressTypeIPv4, Ports: []discoveryv1.EndpointPort{{Name: &portName, Port: &number, Protocol: &protocol}}, Endpoints: []discoveryv1.Endpoint{{Addresses: []string{"10.1.0.2"}, Conditions: discoveryv1.EndpointConditions{Ready: &ready}, TargetRef: &corev1.ObjectReference{APIVersion: "v1", Kind: "Pod", Namespace: "team", Name: "replica", UID: "pod"}}}}}, nil
}
func (r *serviceCLIStub) Admit(_ context.Context, s *corev1.Service) (*corev1.Service, error) {
	return s.DeepCopy(), nil
}
func (r *serviceCLIStub) CreateService(_ context.Context, s *corev1.Service) (*corev1.Service, error) {
	r.creates++
	r.temp = s.DeepCopy()
	r.temp.Name = "check"
	r.temp.UID = "temp"
	r.temp.Spec.ClusterIP = "10.0.0.2"
	r.temp.Spec.ClusterIPs = []string{"10.0.0.2"}
	return r.temp.DeepCopy(), nil
}
func (r *serviceCLIStub) CreatePolicy(_ context.Context, n *networkingv1.NetworkPolicy) (*networkingv1.NetworkPolicy, error) {
	r.creates++
	r.network = n.DeepCopy()
	r.network.Name = "isolate"
	r.network.UID = "net"
	return r.network.DeepCopy(), nil
}
func (r *serviceCLIStub) CreatePod(_ context.Context, p *corev1.Pod) (*corev1.Pod, error) {
	r.creates++
	r.pod = p.DeepCopy()
	r.pod.Name = "check-pod"
	r.pod.UID = "probe"
	return r.pod.DeepCopy(), nil
}
func (r *serviceCLIStub) ReadPod(context.Context, string, string) (*corev1.Pod, error) {
	p := r.pod.DeepCopy()
	p.Status.Phase = corev1.PodSucceeded
	p.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "verifier", Image: p.Spec.Containers[0].Image, ImageID: "sha256:resolved", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0}}}}
	return p, nil
}
func (r *serviceCLIStub) PodLogs(context.Context, string, string, int64) ([]byte, error) {
	var req serviceprobe.Request
	json.Unmarshal([]byte(r.pod.Spec.Containers[0].Args[1]), &req)
	v := serviceprobe.Result{Nonce: req.Nonce}
	for _, target := range req.Targets {
		v.Checks = append(v.Checks, serviceprobe.Check{Target: target, Success: true, Status: req.Contract.ExpectedStatus})
	}
	if r.badLive && r.patches > 0 {
		v.Nonce = "forged"
	}
	return json.Marshal(v)
}
func (r *serviceCLIStub) DeleteOwned(context.Context, servicefix.ObjectRef) error {
	r.deletes++
	return nil
}
func (r *serviceCLIStub) GetServiceDocument(context.Context, string, string) ([]byte, error) {
	return json.Marshal(r.svc)
}
func (r *serviceCLIStub) PatchService(_ context.Context, _, _ string, raw []byte) (*corev1.Service, error) {
	r.patches++
	p, err := jsonpatch.DecodePatch(raw)
	if err != nil {
		return nil, err
	}
	base, _ := json.Marshal(r.svc)
	changed, err := p.Apply(base)
	if err != nil {
		return nil, err
	}
	json.Unmarshal(changed, r.svc)
	r.svc.ResourceVersion = "next"
	return r.svc.DeepCopy(), nil
}

func serviceCLISetup(t *testing.T) (options, *serviceCLIStub, analyzer.Finding) {
	t.Helper()
	file := filepath.Join(t.TempDir(), "policy.yaml")
	raw := `apiVersion: fixora.io/v1alpha1
kind: ServiceRepairPolicy
metadata: {name: test, namespace: team}
spec:
  service: {name: entrance}
  backend: {kind: Deployment, name: unrelated-api}
  selector: {component: api}
  port: {name: public, port: 80, targetPort: web}
  verification:
    method: http
    path: /ready
    expectedStatus: 200
    probeImage: example.invalid/probe@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
`
	if err := os.WriteFile(file, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "entrance", Namespace: "team", UID: "service", ResourceVersion: "first"}, Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeClusterIP, ClusterIP: "10.0.0.1", ClusterIPs: []string{"10.0.0.1"}, Selector: map[string]string{"component": "wrong"}, Ports: []corev1.ServicePort{{Name: "public", Port: 80, Protocol: corev1.ProtocolTCP, TargetPort: intstr.FromInt(9090)}}}}
	return options{output: "text", servicePolicy: file, preview: true, yes: true}, &serviceCLIStub{svc: svc}, analyzer.Finding{ResourceKind: "Service", ResourceName: "entrance", Namespace: "team", ResourceUID: "service", Status: "NoEndpoints", Summary: "Selector does not match ready backends"}
}
func TestServiceCLIApprovedPolicyDynamicAIAndNoAI(t *testing.T) {
	for _, ai := range []bool{false, true} {
		o, r, f := serviceCLISetup(t)
		if ai {
			f.AI = &analyzer.AIResult{Confidence: 95, PatchYAML: "apiVersion: v1\nkind: Service\nmetadata: {name: entrance, namespace: team}\nspec: {selector: {component: api}, ports: [{name: public, port: 80, targetPort: web}]}\n"}
		}
		var out, errout bytes.Buffer
		if code := runServiceFixRuntime(context.Background(), &out, &errout, o, r, f); code != 0 || !strings.Contains(out.String(), "selector") {
			t.Fatal(code, out.String(), errout.String())
		}
	}
	o, r, f := serviceCLISetup(t)
	o.servicePolicy = ""
	var out, errout bytes.Buffer
	runServiceFixRuntime(context.Background(), &out, &errout, o, r, f)
	if !strings.Contains(out.String(), "--service-policy") || r.creates != 0 {
		t.Fatal(out.String())
	}
}
func TestServiceCLIPreviewAndStructuredNoMutation(t *testing.T) {
	for _, format := range []string{"text", "json", "yaml"} {
		o, r, f := serviceCLISetup(t)
		o.output = format
		o.preview = format == "text"
		o.apply = true
		o.shadowVerify = true
		o.delivery = "pr"
		o.repoPath = t.TempDir()
		o.outFile = filepath.Join(o.repoPath, "never.yaml")
		var out, errout bytes.Buffer
		if code := runServiceFixRuntime(context.Background(), &out, &errout, o, r, f); code != 0 || r.creates != 0 || r.patches != 0 {
			t.Fatal(code, out.String(), errout.String())
		}
		if _, err := os.Stat(o.outFile); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("preview wrote source")
		}
	}
}
func TestServiceCLIDeliveryAndRollbackConsent(t *testing.T) {
	o, r, f := serviceCLISetup(t)
	o.preview = false
	o.apply = true
	o.delivery = "cluster"
	r.badLive = true
	var out, errout bytes.Buffer
	if code := runServiceFixRuntime(context.Background(), &out, &errout, o, r, f); code == 0 || r.patches != 1 || !strings.Contains(errout.String(), "rollback") {
		t.Fatal(code, r.patches, out.String(), errout.String())
	}
}
func TestServiceCLISourceClaimsAndScope(t *testing.T) {
	o, r, f := serviceCLISetup(t)
	o.preview = false
	o.delivery = "pr"
	o.repoPath = t.TempDir()
	os.WriteFile(filepath.Join(o.repoPath, "Chart.yaml"), []byte("apiVersion: v2\nname: sample\nversion: 1.0.0\n"), 0600)
	var out, errout bytes.Buffer
	if code := runServiceFixRuntime(context.Background(), &out, &errout, o, r, f); code != 0 || r.creates != 0 || !strings.Contains(out.String(), "manual") {
		t.Fatal(code, out.String(), errout.String())
	}
}

func TestServiceCLIManagedAndConsentGates(t *testing.T) {
	for _, bad := range []string{"managed", "declined", "wrong-policy", "mixed-ai", "no-op"} {
		t.Run(bad, func(t *testing.T) {
			o, r, f := serviceCLISetup(t)
			o.preview = false
			o.delivery = "cluster"
			switch bad {
			case "managed":
				r.svc.Annotations = map[string]string{"meta.helm.sh/release-name": "owned"}
			case "declined":
				o.yes = false
				o.promptInput = bufio.NewReader(strings.NewReader("n\n"))
			case "wrong-policy":
				f.Namespace = "other"
			case "mixed-ai":
				f.AI = &analyzer.AIResult{Confidence: 95, PatchYAML: "apiVersion: v1\nkind: Service\nmetadata: {name: entrance, namespace: team}\nspec: {type: LoadBalancer}"}
			case "no-op":
				r.svc.Spec.Selector = map[string]string{"component": "api"}
				r.svc.Spec.Ports[0].TargetPort = intstr.FromString("web")
			}
			var out, errout bytes.Buffer
			runServiceFixRuntime(context.Background(), &out, &errout, o, r, f)
			if r.creates != 0 || r.patches != 0 {
				t.Fatal("blocked/declined changed cluster", bad)
			}
		})
	}
}
func TestServiceCLISourceSnapshotContainment(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "review.yaml")
	os.WriteFile(path, []byte("first"), 0600)
	captured, err := captureServiceSourceFiles(root, []string{path})
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(path, []byte("second"), 0600)
	now, _ := readServiceSourceFile(path)
	if now == captured[path] {
		t.Fatal("stale source accepted")
	}
	if _, err = captureServiceSourceFiles(root, []string{filepath.Join(t.TempDir(), "outside.yaml")}); err == nil {
		t.Fatal("outside source accepted")
	}
}

func (r *serviceCLIStub) ReadPolicy(context.Context, string, string) (*networkingv1.NetworkPolicy, error) {
	return r.network.DeepCopy(), nil
}
func TestServiceCLIProviderReceivesApprovedProposalIntent(t *testing.T) {
	o, _, f := serviceCLISetup(t)
	enriched, err := withServiceProposalIntent(f, o.servicePolicy)
	if err != nil {
		t.Fatal(err)
	}
	var raw string
	for _, e := range enriched.Evidence {
		if e.Label == "Approved Service routing intent (proposal context only)" {
			raw = e.Value
		}
	}
	if !strings.Contains(raw, "unrelated-api") || !strings.Contains(raw, "component") || strings.Contains(raw, "probeImage") || strings.Contains(raw, "sha256:") {
		t.Fatal("missing intended routing or leaked verifier choice", raw)
	}
	f.Namespace = "elsewhere"
	if _, err = withServiceProposalIntent(f, o.servicePolicy); err == nil {
		t.Fatal("wrong target intent sent to provider")
	}
}

func TestServiceCLIPRRefusesExistingSameFileChanges(t *testing.T) {
	o, r, f := serviceCLISetup(t)
	o.preview = false
	o.delivery = "pr"
	o.repoPath = t.TempDir()
	git := func(args ...string) {
		t.Helper()
		c := exec.Command("git", args...)
		c.Dir = o.repoPath
		if b, e := c.CombinedOutput(); e != nil {
			t.Fatalf("git %v: %v %s", args, e, b)
		}
	}
	git("init")
	git("config", "user.name", "Fixora Test")
	git("config", "user.email", "test@example.invalid")
	os.WriteFile(filepath.Join(o.repoPath, "service.yaml"), []byte("apiVersion: v1\nkind: Service\nmetadata: {name: entrance, namespace: team}\nspec: {selector: {component: wrong}, ports: [{name: public, port: 80, targetPort: 9090}]}\n"), 0600)
	os.WriteFile(filepath.Join(o.repoPath, "other.yaml"), []byte("apiVersion: v1\nkind: ConfigMap\nmetadata: {name: unrelated}\n"), 0600)
	kfile := filepath.Join(o.repoPath, "kustomization.yaml")
	os.WriteFile(kfile, []byte("resources:\n- service.yaml\n"), 0600)
	git("add", ".")
	git("commit", "-m", "base")
	original := []byte("resources:\n- service.yaml\n- other.yaml\n")
	os.WriteFile(kfile, original, 0600)
	var out, errout bytes.Buffer
	code := runServiceFixRuntime(context.Background(), &out, &errout, o, r, f)
	now, _ := os.ReadFile(kfile)
	if code == 0 || r.creates != 0 || !bytes.Equal(now, original) {
		t.Fatalf("existing same-file edits were consumed: code=%d creates=%d output=%s error=%s", code, r.creates, out.String(), errout.String())
	}
}

func (r *serviceCLIStub) FindAttempt(context.Context, string, string, string) ([]servicefix.ObjectRef, error) {
	return nil, errors.New("no uncertain fixture creates")
}
