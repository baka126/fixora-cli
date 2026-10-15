//go:build e2e && e2e_delivery

package e2e

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fixora/kubectl-fixora/internal/analyzer"
	"github.com/fixora/kubectl-fixora/internal/kube"
	"github.com/fixora/kubectl-fixora/internal/servicefix"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Run through hack/e2e-service-probe.sh: image construction and publishing is
// confined to its loopback registry and explicitly selected local kind cluster.
func TestServiceRoutingContractsAndRollback(t *testing.T) {
	image := os.Getenv("FIXORA_E2E_SERVICE_PROBE_IMAGE")
	if image == "" {
		t.Skip("locally built digest-pinned verifier required; run hack/e2e-service-probe.sh")
	}
	for _, backend := range []struct{ kind, name string }{{"Deployment", "ledger-workers"}, {"StatefulSet", "account-replicas"}, {"DaemonSet", "edge-workers"}} {
		t.Run(backend.kind, func(t *testing.T) {
			ns := newNamespace(t)
			applyFixture(t, ns, "service-repair/"+strings.ToLower(backend.kind)+".yaml")
			kubectl(t, "rollout", "status", strings.ToLower(backend.kind)+"/"+backend.name, "-n", ns, "--timeout=180s")
			client, err := kube.NewRequiredTypedClient(kubeContext, "local Service E2E")
			if err != nil {
				t.Fatal(err)
			}
			r, err := servicefix.NewClient(client)
			if err != nil {
				t.Fatal(err)
			}
			for _, port := range []string{"app-web", "8080"} {
				t.Run(port, func(t *testing.T) {
					raw := fmt.Sprintf("apiVersion: fixora.io/v1alpha1\nkind: ServiceRepairPolicy\nmetadata: {name: route, namespace: %s}\nspec:\n  service: {name: routing-entry}\n  backend: {kind: %s, name: %s}\n  selector: {component: arbitrary-api}\n  port: {name: client, port: 80, targetPort: %s}\n  verification:\n    method: http\n    path: /ready\n    expectedStatus: 200\n    probeImage: %s\n", ns, backend.kind, backend.name, port, image)
					file := filepath.Join(t.TempDir(), "policy.yaml")
					if err = os.WriteFile(file, []byte(raw), 0600); err != nil {
						t.Fatal(err)
					}
					a, err := servicefix.LoadPolicy(file)
					if err != nil {
						t.Fatal(err)
					}
					ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
					defer cancel()
					svc, err := r.GetService(ctx, ns, "routing-entry")
					if err != nil {
						t.Fatal(err)
					}
					f := analyzer.Finding{ResourceKind: "Service", ResourceName: svc.Name, ResourceUID: string(svc.UID), Namespace: ns, Status: "NoEndpoints"}
					p, err := servicefix.Prepare(ctx, r, a, f, "")
					if err != nil {
						t.Fatal(err)
					}
					v, err := servicefix.Verify(ctx, r, p)
					if err != nil {
						t.Fatal(v, err)
					}
					applied, err := servicefix.Apply(ctx, r, p, v.Receipt)
					if err != nil {
						t.Fatal(err)
					}
					live, err := servicefix.VerifyLive(ctx, r, p, applied)
					if err != nil || live.Status != "production-contract-passed" {
						t.Fatal(live, err)
					}
					restored, err := servicefix.Restore(ctx, r, applied)
					if err != nil || restored.Status != "rollback-restored" {
						t.Fatal(restored, err)
					}
					// All verifier Pods/policies/temporary Services are gone; real target remains.
					for _, resource := range []string{"pods", "services", "networkpolicies"} {
						out := kubectl(t, "get", resource, "-n", ns, "-l", "fixora.io/service-verifier", "-o", "name")
						if strings.TrimSpace(out) != "" {
							t.Fatal("verifier leftover", resource, out)
						}
					}
				})
			}
			applyFixture(t, ns, "service-repair/refusal.yaml")
			waitFor(t, 30*time.Second, "mixed owner Pod creation", func() bool {
				_, err := client.Clientset.CoreV1().Pods(ns).Get(context.Background(), "unrelated-client", metav1.GetOptions{})
				return err == nil
			})
			raw := fmt.Sprintf("apiVersion: fixora.io/v1alpha1\nkind: ServiceRepairPolicy\nmetadata: {name: refusal, namespace: %s}\nspec:\n  service: {name: routing-entry}\n  backend: {kind: %s, name: %s}\n  selector: {component: arbitrary-api}\n  port: {name: client, port: 80, targetPort: app-web}\n  verification: {method: tcp, probeImage: %s}\n", ns, backend.kind, backend.name, image)
			file := filepath.Join(t.TempDir(), "refusal.yaml")
			os.WriteFile(file, []byte(raw), 0600)
			a, err := servicefix.LoadPolicy(file)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = servicefix.Prepare(context.Background(), r, a, analyzer.Finding{ResourceKind: "Service", ResourceName: "routing-entry", Namespace: ns, Status: "NoEndpoints"}, ""); err == nil {
				t.Fatal("mixed/unready backend approved")
			}
		})
	}
}
