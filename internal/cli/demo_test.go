package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fixora/kubectl-fixora/internal/analyzer"
	"github.com/fixora/kubectl-fixora/internal/fix"
	"github.com/fixora/kubectl-fixora/internal/infer"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/types"
)

type fakeDemoOperations struct {
	namespace       *corev1.Namespace
	deployment      *appsv1.Deployment
	createNSCalls   int
	createNPCalls   int
	createDepCalls  int
	deleteCalls     int
	deleteUID       types.UID
	deleteCtxErr    error
	deploymentError error
	deleteErr       error
	analysisError   error
	verifyCalls     int
	verifyOpts      options
}

func (f *fakeDemoOperations) createNamespace(_ context.Context, ns *corev1.Namespace) (*corev1.Namespace, error) {
	f.createNSCalls++
	f.namespace = ns.DeepCopy()
	f.namespace.UID = "demo-uid"
	return f.namespace, nil
}
func (f *fakeDemoOperations) createDeployment(_ context.Context, d *appsv1.Deployment) error {
	f.createDepCalls++
	if f.deploymentError != nil {
		return f.deploymentError
	}
	f.deployment = d.DeepCopy()
	return nil
}
func (f *fakeDemoOperations) createNetworkPolicy(_ context.Context, p *networkingv1.NetworkPolicy) error {
	f.createNPCalls++
	if p.Labels[demoRunLabel] == "" || len(p.Spec.PolicyTypes) != 2 {
		return errors.New("demo NetworkPolicy must be run-labeled and default deny")
	}
	return nil
}
func (f *fakeDemoOperations) deleteNamespace(ctx context.Context, _ string, uid types.UID) error {
	f.deleteCalls++
	f.deleteUID = uid
	f.deleteCtxErr = ctx.Err()
	return f.deleteErr
}
func (f *fakeDemoOperations) getNamespace(_ context.Context, name string) (*corev1.Namespace, error) {
	if f.namespace == nil || f.namespace.Name != name {
		return nil, errors.New("not found")
	}
	return f.namespace.DeepCopy(), nil
}
func (f *fakeDemoOperations) analyzeVerifyAndReport(ctx context.Context, stdout, stderr io.Writer, opts options, resource, namespace string) int {
	f.verifyCalls++
	f.verifyOpts = opts
	if err := ctx.Err(); err != nil {
		fmt.Fprintf(stderr, "error: demo analysis did not complete: %v\n", err)
		return 1
	}
	if f.analysisError != nil {
		fmt.Fprintf(stderr, "error: demo analysis did not complete: %v\n", f.analysisError)
		return 1
	}
	fmt.Fprintf(stdout, "Demo shadow verification for %s in %s\n", resource, namespace)
	fmt.Fprintln(stdout, "No production delivery or apply.")
	return 0
}

func TestDemoPreviewAndConfirmationRefusalCreateNothing(t *testing.T) {
	fixture := demoFixtures()["config"]
	for _, tc := range []struct {
		name string
		opts options
	}{
		{name: "preview", opts: options{output: "text", preview: true}},
		{name: "confirmation refused", opts: options{output: "text", promptInput: bufio.NewReader(strings.NewReader("no\n"))}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ops := &fakeDemoOperations{}
			var out, errOut strings.Builder
			code := runDemo(context.Background(), &out, &errOut, tc.opts, ops, fixture, func() (string, error) { return "0123456789abcdef", nil })
			if code != 0 || ops.createNSCalls != 0 || ops.createNPCalls != 0 || ops.createDepCalls != 0 || ops.deleteCalls != 0 {
				t.Fatalf("demo should make no changes, code=%d ops=%+v stdout=%q stderr=%q", code, ops, out.String(), errOut.String())
			}
		})
	}
}

func TestDemoPartialCreationCleansOnlyCreatedRunNamespace(t *testing.T) {
	ops := &fakeDemoOperations{deploymentError: errors.New("create denied")}
	var out, errOut strings.Builder
	opts := options{output: "text", yes: true}
	code := runDemo(context.Background(), &out, &errOut, opts, ops, demoFixtures()["probe"], func() (string, error) { return "0123456789abcdef", nil })
	if code == 0 || ops.createNSCalls != 1 || ops.createNPCalls != 1 || ops.createDepCalls != 1 || ops.deleteCalls != 1 {
		t.Fatalf("partial setup should fail and clean up, code=%d ops=%+v", code, ops)
	}
	if ops.deleteUID != "demo-uid" || !strings.Contains(out.String(), "Cleaned up namespace/fixora-demo-0123456789abcdef") {
		t.Fatalf("cleanup should use the returned UID and exact generated namespace: %+v output=%q", ops, out.String())
	}
	if !strings.Contains(errOut.String(), "create demo Deployment") {
		t.Fatalf("creation error should be reported: %q", errOut.String())
	}
}

func TestDemoCleanupRunsAfterCancellationWithIndependentContext(t *testing.T) {
	ops := &fakeDemoOperations{}
	ctx, cancel := context.WithCancel(context.Background())
	var out, errOut strings.Builder
	opts := options{output: "text", yes: true}
	code := runDemo(ctx, &out, &errOut, opts, ops, demoFixtures()["config"], func() (string, error) {
		cancel()
		return "fedcba9876543210", nil
	})
	if code == 0 || ops.deleteCalls != 1 || ops.deleteUID != "demo-uid" || ops.deleteCtxErr != nil {
		t.Fatalf("cleanup must run with its own context after cancellation, code=%d ops=%+v stderr=%q", code, ops, errOut.String())
	}
	if ops.namespace.Labels[demoRunLabel] != "fedcba9876543210" || ops.deployment.Labels[demoRunLabel] != "fedcba9876543210" || ops.deployment.Spec.Template.Labels[demoRunLabel] != "fedcba9876543210" {
		t.Fatalf("created objects must carry the run identity, namespace=%v deployment=%v podTemplate=%v", ops.namespace.Labels, ops.deployment.Labels, ops.deployment.Spec.Template.Labels)
	}
	if !strings.Contains(errOut.String(), "context canceled") {
		t.Fatalf("canceled demo should report analysis cancellation: %q", errOut.String())
	}
}

func TestDemoSuccessfulRunUsesGuardedShadowAndCleansNamespace(t *testing.T) {
	ops := &fakeDemoOperations{}
	var out, errOut strings.Builder
	code := runDemo(context.Background(), &out, &errOut, options{output: "text", yes: true}, ops, demoFixtures()["config"], func() (string, error) { return "0123456789abcdef", nil })
	if code != 0 || ops.deleteCalls != 1 || ops.verifyCalls != 1 || !ops.verifyOpts.shadowVerify || ops.verifyOpts.apply || ops.verifyOpts.delivery != "patch" || !ops.verifyOpts.yes || !strings.Contains(out.String(), "Demo shadow verification for Deployment/demo-config") || !strings.Contains(out.String(), "No production delivery or apply.") || !strings.Contains(out.String(), "Cleaned up namespace/") {
		t.Fatalf("successful demo must use guarded verification without delivery and clean up, code=%d stdout=%q stderr=%q ops=%+v", code, out.String(), errOut.String(), ops)
	}
}

func TestDemoCleanupFailurePrintsRecoveryCommand(t *testing.T) {
	ops := &fakeDemoOperations{deleteErr: errors.New("forbidden")}
	var out, errOut strings.Builder
	code := runDemo(context.Background(), &out, &errOut, options{output: "text", yes: true}, ops, demoFixtures()["oom"], func() (string, error) { return "0123456789abcdef", nil })
	if code == 0 || !strings.Contains(errOut.String(), "kubectl delete namespace fixora-demo-0123456789abcdef") || !strings.Contains(errOut.String(), "fixora.io/demo-run=0123456789abcdef") {
		t.Fatalf("failed cleanup must identify recovery action and namespace, code=%d stderr=%q", code, errOut.String())
	}
}

func TestDemoDeploymentAndFixturesAreRunScoped(t *testing.T) {
	for _, name := range []string{"config", "oom", "probe"} {
		fixture, ok := demoFixtures()[name]
		if !ok || fixture.container.Image == "" {
			t.Fatalf("fixture %q is missing a container image", name)
		}
		d := demoDeployment("fixora-demo-0123456789abcdef", "demo-"+name, "0123456789abcdef", fixture.container)
		if d.Namespace != "fixora-demo-0123456789abcdef" || d.Labels[demoRunLabel] != "0123456789abcdef" || d.Spec.Template.Labels[demoRunLabel] != "0123456789abcdef" {
			t.Fatalf("fixture %q does not retain namespace/run ownership: %#v", name, d)
		}
	}
}

func TestDefaultProbeDemoHasAProvenConcretePortCorrection(t *testing.T) {
	fixture := demoFixtures()["probe"]
	if fixture.container.ReadinessProbe == nil || fixture.container.ReadinessProbe.HTTPGet == nil || fixture.container.ReadinessProbe.HTTPGet.Port.IntValue() != 9876 {
		t.Fatalf("probe demo must retain the intentionally wrong HTTP probe port: %#v", fixture.container)
	}
	if len(fixture.container.Ports) != 1 || fixture.container.Ports[0].ContainerPort != 8080 || !strings.Contains(strings.Join(fixture.container.Command, " "), "httpd -f -p 8080") {
		t.Fatalf("probe demo must expose a known-good HTTP service on its single declared port: %#v", fixture.container)
	}
	finding := analyzer.Finding{ResourceKind: "Deployment", ResourceName: "demo-probe", Namespace: "demo", Status: "ProbeFailure", Evidence: []analyzer.Evidence{
		{Label: "Container image app", Value: fixture.container.Image},
		{Label: "Container ports", Value: "8080"},
		{Label: "Readiness probe port", Value: "9876"},
	}}
	plan, ok := infer.Concrete(context.Background(), nil, finding, fix.BuildPlan(finding))
	if !ok || !plan.ApplyEligible || !strings.Contains(plan.PatchYAML(), "port: 8080") {
		t.Fatalf("default demo fixture should produce a concrete, eligible port patch: ok=%t plan=%#v", ok, plan)
	}
}

func TestDemoRetryPolicyIsBoundedAndReportsMissingProvider(t *testing.T) {
	t.Setenv("FIXORA_CONFIG", filepath.Join(t.TempDir(), "missing-config.json"))
	t.Setenv("FIXORA_AI_PROVIDER", "noop")
	available, ok := demoRetryPolicy(options{useAI: true})
	if !ok || available.shadowRetries != 2 || !available.useAI {
		t.Fatalf("configured AI should enable at most two revisions, got opts=%+v available=%t", available, ok)
	}
	t.Setenv("FIXORA_AI_PROVIDER", "openai")
	t.Setenv("FIXORA_AI_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	unavailable, ok := demoRetryPolicy(options{useAI: true})
	if ok || unavailable.shadowRetries != 0 || unavailable.useAI {
		t.Fatalf("missing provider credentials must force one deterministic attempt, got opts=%+v available=%t", unavailable, ok)
	}
}

func TestDemoWaitsForOwnedPodEvidenceBeforePreview(t *testing.T) {
	for _, tc := range []struct {
		status string
		want   bool
	}{
		{status: "NoOwnedPods", want: false},
		{status: "Healthy", want: false},
		{status: "OwnedPodsHealthy", want: false},
		{status: "Pending", want: false},
		{status: "CreateContainerConfigError", want: true},
		{status: "ProbeFailure", want: true},
	} {
		t.Run(tc.status, func(t *testing.T) {
			if got := demoFindingReady(analyzer.Finding{Status: tc.status}, nil); got != tc.want {
				t.Fatalf("demoFindingReady(%q) = %v, want %v", tc.status, got, tc.want)
			}
		})
	}
	if demoFindingReady(analyzer.Finding{Status: "ProbeFailure"}, errors.New("read failed")) {
		t.Fatal("analysis errors must not be treated as ready evidence")
	}
}
