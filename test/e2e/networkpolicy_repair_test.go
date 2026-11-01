//go:build e2e_delivery

package e2e

import (
	"context"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/fixora/kubectl-fixora/internal/kube"
	"github.com/fixora/kubectl-fixora/internal/networkpolicyfix"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestNetworkPolicyRepairEnforcingCNI(t *testing.T) {
	if os.Getenv("FIXORA_RUN_NETWORKPOLICY_KIND_E2E") != "1" {
		t.Skip("run only from hack/e2e-networkpolicy.sh")
	}
	probeImage := os.Getenv("FIXORA_NETWORKPOLICY_PROBE_IMAGE")
	if !strings.Contains(probeImage, "@sha256:") {
		t.Fatal("harness did not provide the pushed digest-pinned probe image")
	}
	client, err := kube.NewRequiredTypedClient("", "local NetworkPolicy acceptance test")
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(client.Config.Host)
	if err != nil || net.ParseIP(parsed.Hostname()) == nil || !net.ParseIP(parsed.Hostname()).IsLoopback() {
		t.Fatalf("refusing non-loopback Kubernetes API endpoint %q", client.Config.Host)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	ns, err := client.Clientset.CoreV1().Namespaces().Get(ctx, "fixora-np-e2e", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if ns.Labels["fixora.io/networkpolicy-e2e"] != "enabled" {
		t.Fatal("target namespace is not marked as the disposable acceptance fixture")
	}
	policy, err := client.Clientset.NetworkingV1().NetworkPolicies(ns.Name).Get(ctx, "api-policy", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	original := *policy.Spec.DeepCopy()
	authorityBytes, err := os.ReadFile(filepath.Join("fixtures", "networkpolicy-repair", "authority.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	authorityBytes = []byte(strings.ReplaceAll(string(authorityBytes), "probe.invalid/fixora-service-probe@sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc", probeImage))
	authorityPath := filepath.Join(t.TempDir(), "authority.yaml")
	if err = os.WriteFile(authorityPath, authorityBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	authority, err := networkpolicyfix.LoadAuthority(authorityPath)
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := networkpolicyfix.NewClient(client)
	if err != nil {
		t.Fatal(err)
	}
	target := networkpolicyfix.Target{Namespace: ns.Name, Name: policy.Name}
	plan, err := networkpolicyfix.Prepare(ctx, runtime, authority, target)
	if err != nil {
		t.Fatal(err)
	}
	verification, err := networkpolicyfix.Verify(ctx, runtime, plan)
	if err != nil {
		t.Fatalf("CNI policy verification failed: %s: %v", verification.Status, err)
	}
	if len(verification.Cases) != 4 || verification.Controls["noPolicyAllow"] != "passed" || verification.Controls["defaultDeny"] != "passed" {
		t.Fatalf("incomplete CNI matrix: %+v", verification)
	}
	applied, err := networkpolicyfix.Apply(ctx, runtime, plan, verification.Receipt)
	if err != nil {
		t.Fatal(err)
	}
	observed, err := networkpolicyfix.Observe(ctx, runtime, applied)
	if err != nil || observed.Status != "policy-spec-observed" {
		t.Fatalf("policy observation failed: %+v %v", observed, err)
	}
	restored, err := networkpolicyfix.Restore(ctx, runtime, applied, true)
	if err != nil || restored.Status != "original-policy-spec-restored" {
		t.Fatalf("explicit restore failed: %+v %v", restored, err)
	}
	after, err := runtime.GetPolicy(ctx, target.Namespace, target.Name)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(after.Spec, original) {
		t.Fatal("acceptance test did not restore the fixture policy")
	}
}
