package infer

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/fixora/kubectl-fixora/internal/kube"
	"strings"
	"testing"

	"github.com/fixora/kubectl-fixora/internal/analyzer"
	"github.com/fixora/kubectl-fixora/internal/fix"
)

type dependencyReader struct {
	kubeReader
	objects map[string]map[string]any
	slices  []map[string]any
}

func (r dependencyReader) GetResource(_ context.Context, ns, res string) (map[string]any, error) {
	if ns != "demo" {
		panic("unexpected namespace")
	}
	p, ok := r.objects[res]
	if !ok {
		return nil, fmt.Errorf("missing %s", res)
	}
	return p, nil
}
func (r dependencyReader) GetResourceItems(_ context.Context, ns string, all bool, res string) ([]map[string]any, error) {
	if ns != "demo" || all || res != "endpointslices.discovery.k8s.io" {
		panic("unexpected endpoint read")
	}
	return r.slices, nil
}
func depFixture(t *testing.T) (dependencyReader, analyzer.Finding) {
	t.Helper()
	decode := func(text string) map[string]any {
		var o map[string]any
		if err := json.Unmarshal([]byte(text), &o); err != nil {
			t.Fatal(err)
		}
		return o
	}
	c := `{"name":"client","image":"busybox:1.36","command":["sh","-c","while true; do nc -z -w 3 $DB_HOST $DB_PORT || exit 1; sleep 60; done"],"env":[{"name":"DB_HOST","value":"wrong-host"},{"name":"DB_PORT","value":"1234"}]}`
	r := dependencyReader{objects: map[string]map[string]any{
		"pod/client-pod":       decode(`{"apiVersion":"v1","kind":"Pod","metadata":{"uid":"p","name":"client-pod","namespace":"demo","ownerReferences":[{"kind":"ReplicaSet","apiVersion":"apps/v1","name":"client-rs","uid":"r","controller":true}]},"spec":{"containers":[` + c + `]},"status":{"containerStatuses":[{"name":"client","ready":false,"state":{"waiting":{"reason":"CrashLoopBackOff"}},"lastState":{"terminated":{"reason":"Error","exitCode":1}}}]}}`),
		"replicaset/client-rs": decode(`{"apiVersion":"apps/v1","kind":"ReplicaSet","metadata":{"uid":"r","name":"client-rs","namespace":"demo","ownerReferences":[{"kind":"Deployment","apiVersion":"apps/v1","name":"client","uid":"d","controller":true}]}}`),
		"deployment/client":    decode(`{"apiVersion":"apps/v1","kind":"Deployment","metadata":{"uid":"d","name":"client","namespace":"demo","annotations":{"fixora.io/connection-configmap":"connection"}},"spec":{"template":{"spec":{"containers":[` + c + `]}}}}`),
		"configmap/connection": decode(`{"metadata":{"name":"connection","namespace":"demo"},"data":{"DB_HOST":"database","DB_PORT":"5432"}}`),
		"service/database":     decode(`{"metadata":{"name":"database","namespace":"demo"},"spec":{"ports":[{"name":"db","protocol":"TCP","port":5432,"targetPort":5432}]}}`),
	}, slices: []map[string]any{decode(`{"metadata":{"namespace":"demo","labels":{"kubernetes.io/service-name":"database"}},"ports":[{"name":"db","protocol":"TCP","port":5432}],"endpoints":[{"addresses":["10.0.0.4"],"conditions":{"ready":true}}]}`)}}
	f := analyzer.Finding{Namespace: "demo", ResourceKind: "Deployment", ResourceName: "client", PodName: "client-pod", Status: "CrashLoopBackOff", Evidence: []analyzer.Evidence{{Label: "Failing container", Value: "client"}, {Label: "Container image client", Value: "busybox:1.36"}}, Logs: []analyzer.LogSnippet{{Text: "nc: bad address wrong-host; connection refused"}}}
	return r, f
}
func TestDependencyInferrerUsesApprovedReadyService(t *testing.T) {
	r, f := depFixture(t)
	result, ok, err := dependencyInferrer{}.Infer(context.Background(), r, f, fix.BuildPlan(f))
	if !ok || err != nil {
		t.Fatalf("expected approved endpoint repair: %+v ok=%v err=%v", result, ok, err)
	}
	p, ok := Concrete(context.Background(), r, f, fix.BuildPlan(f))
	if !ok || !p.ApplyEligible || p.Strategy != "env" {
		t.Fatalf("expected eligible connection env patch: %+v", p)
	}
}
func TestDependencyInferrerDeclinesUnprovenConnection(t *testing.T) {
	for _, scenario := range []string{"missing-annotation", "wrong-pod", "wrong-owner", "different-template", "wrong-configmap-namespace", "external-host", "wrong-port", "unready-endpoints", "other-service-endpoints", "wrong-endpoint-port", "missing-logs", "ready-pod", "secret-env", "already-correct"} {
		t.Run(scenario, func(t *testing.T) {
			r, f := depFixture(t)
			pod := r.objects["pod/client-pod"]
			spec := pod["spec"].(map[string]any)
			c := spec["containers"].([]any)[0].(map[string]any)
			cfg := r.objects["configmap/connection"]["data"].(map[string]any)
			switch scenario {
			case "missing-annotation":
				r.objects["deployment/client"]["metadata"].(map[string]any)["annotations"] = map[string]any{}
			case "wrong-pod":
				pod["metadata"].(map[string]any)["name"] = "different"
			case "wrong-owner":
				r.objects["replicaset/client-rs"]["metadata"].(map[string]any)["ownerReferences"].([]any)[0].(map[string]any)["name"] = "different"
			case "different-template":
				c["image"] = "other:1"
			case "wrong-configmap-namespace":
				r.objects["configmap/connection"]["metadata"].(map[string]any)["namespace"] = "other"
			case "external-host":
				cfg["DB_HOST"] = "database.other.svc.cluster.local"
			case "wrong-port":
				cfg["DB_PORT"] = "5433"
			case "unready-endpoints":
				r.slices[0]["endpoints"].([]any)[0].(map[string]any)["conditions"].(map[string]any)["ready"] = false
			case "other-service-endpoints":
				r.slices[0]["metadata"].(map[string]any)["labels"].(map[string]any)["kubernetes.io/service-name"] = "other"
			case "wrong-endpoint-port":
				r.slices[0]["ports"].([]any)[0].(map[string]any)["port"] = float64(5433)
			case "missing-logs":
				f.Logs = nil
			case "ready-pod":
				pod["status"].(map[string]any)["containerStatuses"].([]any)[0].(map[string]any)["ready"] = true
			case "secret-env":
				c["env"].([]any)[0].(map[string]any)["valueFrom"] = map[string]any{"secretKeyRef": map[string]any{"name": "private", "key": "host"}}
			case "already-correct":
				for _, item := range c["env"].([]any) {
					e := item.(map[string]any)
					e["value"] = cfg[e["name"].(string)]
				}
			}

			if scenario == "secret-env" || scenario == "already-correct" {
				r.objects["deployment/client"]["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)["containers"].([]any)[0].(map[string]any)["env"] = c["env"]
			}
			if _, ok, _ := (dependencyInferrer{}).Infer(context.Background(), r, f, fix.BuildPlan(f)); ok {
				t.Fatal("unproven connection repair accepted")
			}
		})
	}
}

func TestDependencyInferrerRequiresNCToConsumeConnectionVariables(t *testing.T) {
	r, f := depFixture(t)
	command := []any{"sh", "-c", "echo nc -z wrong-host 1234; echo $DB_HOST $DB_PORT; exit 1"}
	r.objects["pod/client-pod"]["spec"].(map[string]any)["containers"].([]any)[0].(map[string]any)["command"] = command
	r.objects["deployment/client"]["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)["containers"].([]any)[0].(map[string]any)["command"] = command
	if _, ok, _ := (dependencyInferrer{}).Infer(context.Background(), r, f, fix.BuildPlan(f)); ok {
		t.Fatal("echoing nc and unrelated endpoint variables cannot prove consumption")
	}
}

func TestDependencyProposalAcrossControllerKinds(t *testing.T) {
	for _, kind := range []string{"Deployment", "StatefulSet", "DaemonSet", "ReplicaSet", "Job", "CronJob", "Pod"} {
		t.Run(kind, func(t *testing.T) {
			r, f := depFixture(t)
			f.ResourceKind = kind
			old := r.objects["deployment/client"]
			template, _ := kube.WorkloadPodSpec(old)
			target, _ := kube.WorkloadPatch(kind, "client", "demo", template)
			target["metadata"].(map[string]any)["uid"] = "d"
			target["metadata"].(map[string]any)["annotations"] = map[string]any{"fixora.io/connection-configmap": "connection"}
			pod := r.objects["pod/client-pod"]
			switch kind {
			case "Deployment":
			case "CronJob":
				job, _ := kube.WorkloadPatch("Job", "client-job", "demo", template)
				job["metadata"].(map[string]any)["uid"] = "j"
				job["metadata"].(map[string]any)["ownerReferences"] = []any{map[string]any{"kind": "CronJob", "name": "client", "uid": "d", "apiVersion": "batch/v1", "controller": true}}
				r.objects["job/client-job"] = job
				pod["metadata"].(map[string]any)["ownerReferences"] = []any{map[string]any{"kind": "Job", "name": "client-job", "uid": "j", "apiVersion": "batch/v1", "controller": true}}
			case "Pod":
				f.ResourceName = "client-pod"
				target = pod
				meta := target["metadata"].(map[string]any)
				delete(meta, "ownerReferences")
				meta["annotations"] = map[string]any{"fixora.io/connection-configmap": "connection"}
			default:
				pod["metadata"].(map[string]any)["ownerReferences"] = []any{map[string]any{"kind": kind, "name": "client", "uid": "d", "apiVersion": kube.WorkloadVersion(kind), "controller": true}}
			}
			r.objects[strings.ToLower(kind)+"/"+f.ResourceName] = target
			p, ok := Concrete(context.Background(), r, f, fix.BuildPlan(f))
			if !ok || p.Strategy != "env" || strings.Contains(p.PatchYAML(), "TODO_") {
				t.Fatalf("no concrete proposal: %+v", p)
			}
			want := kind != "Pod" && kind != "Job" && kind != "ReplicaSet"
			if p.ApplyEligible != want {
				t.Fatalf("eligible %v want %v", p.ApplyEligible, want)
			}
		})
	}
}
