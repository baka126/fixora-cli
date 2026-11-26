package cli

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/fixora/kubectl-fixora/internal/ai"
	"github.com/fixora/kubectl-fixora/internal/analyzer"
	"github.com/fixora/kubectl-fixora/internal/kube"
	"github.com/fixora/kubectl-fixora/internal/termui"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
)

const demoRunLabel = "fixora.io/demo-run"

type demoFixture struct {
	name        string
	description string
	container   corev1.Container
}

type demoOperations interface {
	createNamespace(context.Context, *corev1.Namespace) (*corev1.Namespace, error)
	createNetworkPolicy(context.Context, *networkingv1.NetworkPolicy) error
	createDeployment(context.Context, *appsv1.Deployment) error
	deleteNamespace(context.Context, string, types.UID) error
	getNamespace(context.Context, string) (*corev1.Namespace, error)
	analyzeVerifyAndReport(context.Context, io.Writer, io.Writer, options, string, string) int
}

type typedDemoOperations struct{ client *kube.TypedClient }

func (o typedDemoOperations) createNamespace(ctx context.Context, ns *corev1.Namespace) (*corev1.Namespace, error) {
	return o.client.CreateNamespace(ctx, ns)
}
func (o typedDemoOperations) createDeployment(ctx context.Context, deployment *appsv1.Deployment) error {
	if o.client.Clientset == nil {
		return fmt.Errorf("typed Kubernetes client is not configured")
	}
	_, err := o.client.Clientset.AppsV1().Deployments(deployment.Namespace).Create(ctx, deployment, metav1.CreateOptions{})
	return err
}
func (o typedDemoOperations) createNetworkPolicy(ctx context.Context, policy *networkingv1.NetworkPolicy) error {
	if o.client.Clientset == nil {
		return fmt.Errorf("typed Kubernetes client is not configured")
	}
	_, err := o.client.Clientset.NetworkingV1().NetworkPolicies(policy.Namespace).Create(ctx, policy, metav1.CreateOptions{})
	return err
}
func (o typedDemoOperations) deleteNamespace(ctx context.Context, name string, uid types.UID) error {
	return o.client.DeleteNamespaceUID(ctx, name, uid)
}
func (o typedDemoOperations) getNamespace(ctx context.Context, name string) (*corev1.Namespace, error) {
	return o.client.GetNamespace(ctx, name)
}
func (o typedDemoOperations) analyzeVerifyAndReport(ctx context.Context, stdout, stderr io.Writer, opts options, resource, namespace string) int {
	reader := o.client
	opts.namespace = namespace
	analysisCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	var finding analyzer.Finding
	var err error
	for {
		an := analyzer.New(reader, analyzer.Options{Namespace: opts.namespace, IncludeLogs: true, Redact: true})
		finding, err = an.AnalyzeResource(analysisCtx, resource)
		if demoFindingReady(finding, err) {
			break
		}
		select {
		case <-analysisCtx.Done():
			if err != nil {
				fmt.Fprintf(stderr, "error: demo analysis did not complete: %v\n", err)
			} else {
				fmt.Fprintf(stderr, "error: demo fixture did not reach actionable owned-Pod evidence before timeout (last status: %s)\n", finding.Status)
			}
			return 1
		case <-time.After(2 * time.Second):
		}
	}
	_, plan, preparedOpts, err := prepareAnalyzedFixPlan(analysisCtx, reader, reader.Fallback, opts, resource, finding, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "error: prepare demo repair preview: %v\n", err)
		return 1
	}
	preparedOpts.preview = false
	preparedOpts.shadowVerify = true
	preparedOpts.apply = false
	preparedOpts.yes = true // demo consent is already scoped to its temporary namespace
	preparedOpts.delivery = "patch"
	preparedOpts.keepShadow = false
	preparedOpts.shadowShell = false
	preparedOpts, retryAvailable := demoRetryPolicy(preparedOpts)
	if !retryAvailable {
		fmt.Fprintln(stdout, "AI retry is off or unavailable; running one deterministic shadow attempt.")
	}
	result, verified, code := verifyInShadow(analysisCtx, stdout, stderr, preparedOpts, reader.Fallback, finding, plan, false)
	if result.Status != "" {
		fmt.Fprintln(stdout)
		fmt.Fprintln(stdout, evidenceCard(finding, plan, result, verified))
	}
	if verified {
		fmt.Fprintln(stdout, "Demo verified in isolation. No patch was delivered and the fixture was not changed.")
	}
	return code
}

func demoRetryPolicy(opts options) (options, bool) {
	if !opts.useAI {
		opts.shadowRetries = 0
		return opts, false
	}
	if _, err := ai.NewFromEnv(); err != nil {
		opts.useAI = false
		opts.shadowRetries = 0
		return opts, false
	}
	if !opts.visited["shadow-retries"] {
		opts.shadowRetries = 2
	}
	return opts, opts.shadowRetries > 0
}

func demoFindingReady(finding analyzer.Finding, err error) bool {
	if err != nil {
		return false
	}
	switch finding.Status {
	case "Healthy", "OwnedPodsHealthy", "NoOwnedPods", "Pending":
		// The controller may be visible before its first Pod exists. Continue
		// polling until there is actionable owned-Pod evidence or the bounded
		// wait expires. Pending is transient for these fixtures and often means
		// the demo cluster has no spare capacity.
		return false
	default:
		return true
	}
}

func runDemoCommand(ctx context.Context, stdout, stderr io.Writer, opts options, args []string) int {
	if len(args) > 1 {
		fmt.Fprintln(stderr, "error: demo accepts at most one fixture: oom, config, or probe")
		return 2
	}
	fixtureName := "probe"
	if len(args) == 1 {
		fixtureName = strings.ToLower(strings.TrimSpace(args[0]))
	}
	fixture, ok := demoFixtures()[fixtureName]
	if !ok {
		fmt.Fprintf(stderr, "error: unknown demo fixture %q; choose oom, config, or probe\n", fixtureName)
		return 2
	}
	if opts.output != "text" {
		fmt.Fprintln(stderr, "error: demo currently supports text output only")
		return 2
	}
	client, err := kube.NewRequiredTypedClient(opts.context, "disposable demo")
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	return runDemo(ctx, stdout, stderr, opts, typedDemoOperations{client}, fixture, newDemoRunID)
}

func runDemo(ctx context.Context, stdout, stderr io.Writer, opts options, ops demoOperations, fixture demoFixture, makeRunID func() (string, error)) (exitCode int) {
	runID, err := makeRunID()
	if err != nil {
		fmt.Fprintf(stderr, "error: create demo run identity: %v\n", err)
		return 1
	}
	namespace := "fixora-demo-" + runID
	if opts.output == "text" {
		fmt.Fprintf(stdout, "Fixora disposable demo: %s\nNamespace: %s\n", fixture.description, namespace)
		fmt.Fprintln(stdout, "Creates one isolated Deployment, verifies a proposed repair in a separate shadow, then deletes the namespace.")
		fmt.Fprintln(stdout, "The demo never applies a repair to the cluster.")
	}
	if opts.preview {
		if opts.output == "text" {
			fmt.Fprintln(stdout, "Preview only; no changes made. Re-run without --preview to continue.")
		}
		return 0
	}
	if !opts.yes && !termui.Confirm("Create the disposable demo workload? It will be deleted when this command ends.", inputFor(opts), stdout) {
		fmt.Fprintln(stdout, "Demo cancelled; no changes made.")
		return 0
	}

	labels := map[string]string{"app.kubernetes.io/managed-by": "fixora", "fixora.io/demo": "true", demoRunLabel: runID}
	created, err := ops.createNamespace(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace, Labels: labels}})
	if err != nil {
		fmt.Fprintf(stderr, "error: create demo namespace: %v\n", err)
		return 1
	}
	uid := created.UID
	if uid == "" {
		// Kubernetes assigns UIDs on create. Fetch the server object before proceeding
		// so cleanup can be pinned to this exact namespace incarnation.
		current, getErr := ops.getNamespace(ctx, namespace)
		if getErr != nil || current.UID == "" {
			fmt.Fprintf(stderr, "error: cannot establish demo namespace identity; refusing to create workloads\n")
			fmt.Fprintf(stderr, "If namespace/%s exists, verify its fixora.io/demo-run=%s label before removing it.\n", namespace, runID)
			return 1
		}
		uid = current.UID
	}
	cleanup := func() error {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		return ops.deleteNamespace(cleanupCtx, namespace, uid)
	}
	defer func() {
		if err := cleanup(); err != nil {
			fmt.Fprintf(stderr, "error: demo namespace cleanup failed for %s; verify its fixora.io/demo-run=%s label, then remove it with `kubectl delete namespace %s`\n", namespace, runID, namespace)
			exitCode = 1
		} else if opts.output == "text" {
			fmt.Fprintf(stdout, "Cleaned up namespace/%s.\n", namespace)
		}
	}()
	policyTypes := []networkingv1.PolicyType{networkingv1.PolicyTypeIngress, networkingv1.PolicyTypeEgress}
	policy := &networkingv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{Name: "demo-default-deny", Namespace: namespace, Labels: labels}, Spec: networkingv1.NetworkPolicySpec{PodSelector: metav1.LabelSelector{}, PolicyTypes: policyTypes}}
	if err := ops.createNetworkPolicy(ctx, policy); err != nil {
		fmt.Fprintf(stderr, "error: create isolated demo NetworkPolicy: %v\n", err)
		return 1
	}

	deploymentName := "demo-" + fixture.name
	deployment := demoDeployment(namespace, deploymentName, runID, fixture.container)
	if err := ops.createDeployment(ctx, deployment); err != nil {
		fmt.Fprintf(stderr, "error: create demo Deployment: %v\n", err)
		return 1
	}
	if opts.output == "text" {
		fmt.Fprintf(stdout, "Created Deployment/%s. Waiting for analyzer evidence...\n", deploymentName)
	}
	demoOpts := opts
	demoOpts.shadowVerify = true
	demoOpts.apply = false
	demoOpts.yes = true         // the interactive consent above is scoped to this temporary demo only
	demoOpts.delivery = "patch" // the verifier returns a verdict without invoking the delivery path
	demoOpts.keepShadow = false
	demoOpts.shadowShell = false
	demoOpts.outFile = ""
	return ops.analyzeVerifyAndReport(ctx, stdout, stderr, demoOpts, "Deployment/"+deploymentName, namespace)
}

func demoFixtures() map[string]demoFixture {
	return map[string]demoFixture{
		"config": {name: "config", description: "missing ConfigMap reference", container: corev1.Container{Name: "app", Image: "busybox:1.36", Command: []string{"sh", "-c", "echo $APP_MODE; sleep 3600"}, Env: []corev1.EnvVar{{Name: "APP_MODE", ValueFrom: &corev1.EnvVarSource{ConfigMapKeyRef: &corev1.ConfigMapKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "missing-config"}, Key: "mode"}}}}}},
		"oom":    {name: "oom", description: "memory usage exceeds its configured limit", container: corev1.Container{Name: "app", Image: "busybox:1.36", Command: []string{"sh", "-c", "exec yes x"}, Resources: corev1.ResourceRequirements{Limits: corev1.ResourceList{"memory": resource.MustParse("16Mi")}}}},
		"probe":  {name: "probe", description: "readiness probe targets the wrong declared port", container: corev1.Container{Name: "app", Image: "busybox:1.36", Command: []string{"sh", "-c", "mkdir -p /tmp/www && echo ok >/tmp/www/index.html && exec httpd -f -p 8080 -h /tmp/www"}, Ports: []corev1.ContainerPort{{Name: "http", ContainerPort: 8080}}, ReadinessProbe: &corev1.Probe{ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: "/", Port: intstr.FromInt(9876)}}, PeriodSeconds: 2, FailureThreshold: 1}}},
	}
}

func demoDeployment(namespace, name, runID string, container corev1.Container) *appsv1.Deployment {
	labels := map[string]string{"app": name, demoRunLabel: runID}
	replicas := int32(1)
	return &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, Labels: labels}, Spec: appsv1.DeploymentSpec{Replicas: &replicas, Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": name}}, Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: labels}, Spec: corev1.PodSpec{Containers: []corev1.Container{container}}}}}
}

func newDemoRunID() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
