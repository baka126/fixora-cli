//go:build e2e && e2e_delivery

package e2e

import (
	"bytes"
	"context"
	"fmt"
	"github.com/fixora/kubectl-fixora/internal/kube"
	"github.com/fixora/kubectl-fixora/internal/pdbfix"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestPDBControllerContractsAndRollback(t *testing.T) {
	if os.Getenv("FIXORA_E2E_PDB") != "1" {
		t.Skip("dedicated local PDB harness required: hack/e2e-pdb.sh")
	}
	if !strings.HasPrefix(kubeContext, "kind-fixora-pdb-e2e-") {
		t.Fatal("refusing PDB mutations outside dedicated local kind context")
	}
	for _, kind := range []string{"Deployment", "StatefulSet"} {
		t.Run(kind, func(t *testing.T) {
			ns := newNamespace(t)
			raw, e := os.ReadFile(filepath.Join("fixtures", "pdb-repair", strings.ToLower(kind)+".yaml"))
			if e != nil {
				t.Fatal(e)
			}
			image := envOr("FIXORA_E2E_PDB_IMAGE", "public.ecr.aws/docker/library/busybox:1.36")
			raw = bytes.ReplaceAll(raw, []byte("FIXORA_TEST_IMAGE"), []byte(image))
			cmd := exec.Command("kubectl", "--context", kubeContext, "apply", "-n", ns, "-f", "-")
			cmd.Env = append(os.Environ(), "KUBECONFIG="+kubeconfig)
			cmd.Stdin = bytes.NewReader(raw)
			if b, e := cmd.CombinedOutput(); e != nil {
				t.Fatal(e, string(b))
			}
			kubectl(t, "--context", kubeContext, "rollout", "status", strings.ToLower(kind)+"/replicas", "-n", ns, "--timeout=180s")
			client, e := kube.NewRequiredTypedClient(kubeContext, "dedicated local PDB acceptance")
			if e != nil {
				t.Fatal(e)
			}
			r, e := pdbfix.NewClient(client)
			if e != nil {
				t.Fatal(e)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
			defer cancel()
			waitPDB := func() {
				waitFor(t, 30*time.Second, "PDB status reconciliation", func() bool {
					p, e := r.GetPDB(ctx, ns, "protection")
					return e == nil && p.Status.ObservedGeneration == p.Generation
				})
			}
			waitPDB()
			before := kubectl(t, "--context", kubeContext, "get", "pods", "-n", ns, "-o", "jsonpath={range .items[*]}{.metadata.uid}{\"\\n\"}{end}")
			policyFile := filepath.Join(t.TempDir(), "approved.yaml")
			policyText := func(budget string, floor, ceiling int) string {
				return fmt.Sprintf("apiVersion: fixora.io/v1alpha1\nkind: PDBRepairPolicy\nmetadata: {name: approval, namespace: %s}\nspec:\n  pdb: {name: protection}\n  workload: {kind: %s, name: replicas}\n  selector: {matchLabels: {component: ledger}}\n  budget: {%s}\n  availability: {minimumHealthyReplicas: %d, maximumConcurrentDisruptions: %d}\n", ns, kind, budget, floor, ceiling)
			}
			execute := func(text string) {
				t.Helper()
				if e := os.WriteFile(policyFile, []byte(text), 0600); e != nil {
					t.Fatal(e)
				}
				a, e := pdbfix.LoadPolicy(policyFile)
				if e != nil {
					t.Fatal(e)
				}
				p, e := pdbfix.Prepare(ctx, r, a, "")
				if e != nil {
					t.Fatal(e)
				}
				v, e := pdbfix.Verify(ctx, r, p)
				if e != nil {
					t.Fatal(e)
				}
				ap, result, e := pdbfix.Apply(ctx, r, p, v)
				if e != nil {
					t.Fatal(result, e)
				}
				result, e = pdbfix.Observe(ctx, r, p, ap)
				if e != nil || result.Status != "policy-converged" {
					t.Fatal(result, e)
				}
				live, e := r.GetPDB(ctx, ns, "protection")
				if e != nil {
					t.Fatal(e)
				}
				if live.Status.DesiredHealthy != 2 || live.Status.DisruptionsAllowed != 1 {
					t.Fatalf("unexpected actual controller rounding %+v", live.Status)
				}
				result, e = pdbfix.Restore(ctx, r, ap)
				if e != nil || result.Status != "restored" {
					t.Fatal(result, e)
				}
			}
			if e := os.WriteFile(policyFile, []byte(policyText("minAvailable: 2", 2, 1)), 0600); e != nil {
				t.Fatal(e)
			}
			outFile := filepath.Join(t.TempDir(), "never-written")
			original, e := r.GetPDB(ctx, ns, "protection")
			if e != nil {
				t.Fatal(e)
			}
			_, errout, code := fixora(t, "fix", "pdb/protection", "-n", ns, "--context", kubeContext, "--pdb-policy", policyFile, "--preview", "--apply", "--out", outFile)
			if code != 0 {
				t.Fatal(errout)
			}
			afterPreview, e := r.GetPDB(ctx, ns, "protection")
			if e != nil || !reflect.DeepEqual(original.Spec, afterPreview.Spec) || original.Generation != afterPreview.Generation {
				t.Fatal("preview changed policy", e)
			}
			if _, e := os.Stat(outFile); !os.IsNotExist(e) {
				t.Fatal("preview wrote file")
			}
			execute(policyText("minAvailable: 2", 2, 1)) // empty-match correction, then inverse.
			kubectl(t, "--context", kubeContext, "patch", "pdb/protection", "-n", ns, "--type=json", "-p", `[{"op":"replace","path":"/spec/selector","value":{"matchLabels":{"component":"ledger"}}}]`)
			waitPDB()
			execute(policyText(`minAvailable: "50%"`, 2, 1))
			execute(policyText("maxUnavailable: 1", 2, 1)) // atomic min→max switch and inverse.
			a, e := pdbfix.LoadPolicy(policyFile)
			if e != nil {
				t.Fatal(e)
			}
			current, e := r.GetPDB(ctx, ns, "protection")
			if e != nil {
				t.Fatal(e)
			}
			other := current.DeepCopy()
			other.ObjectMeta = metav1.ObjectMeta{Name: "overlap", Namespace: ns}
			if _, e = client.Clientset.PolicyV1().PodDisruptionBudgets(ns).Create(ctx, other, metav1.CreateOptions{}); e != nil {
				t.Fatal(e)
			}
			if _, e = pdbfix.Prepare(ctx, r, a, ""); e == nil {
				t.Fatal("overlap authorized")
			}
			if e = client.Clientset.PolicyV1().PodDisruptionBudgets(ns).Delete(ctx, "overlap", metav1.DeleteOptions{}); e != nil {
				t.Fatal(e)
			}
			min := int32(3)
			h := &autoscalingv2.HorizontalPodAutoscaler{ObjectMeta: metav1.ObjectMeta{Name: "active-scale", Namespace: ns}, Spec: autoscalingv2.HorizontalPodAutoscalerSpec{ScaleTargetRef: autoscalingv2.CrossVersionObjectReference{APIVersion: "apps/v1", Kind: kind, Name: "replicas"}, MinReplicas: &min, MaxReplicas: 3}}
			if _, e = client.Clientset.AutoscalingV2().HorizontalPodAutoscalers(ns).Create(ctx, h, metav1.CreateOptions{}); e != nil {
				t.Fatal(e)
			}
			if _, e = pdbfix.Prepare(ctx, r, a, ""); e == nil {
				t.Fatal("active HPA authorized")
			}
			after := kubectl(t, "--context", kubeContext, "get", "pods", "-n", ns, "-o", "jsonpath={range .items[*]}{.metadata.uid}{\"\\n\"}{end}")
			if before != after {
				t.Fatal("PDB repair recreated or evicted Pods")
			}
		})
	}
}
