package hpafix

import (
	"context"
	"errors"
	"testing"
	"time"

	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
)

func authorizedRuntime(t *testing.T) (Plan, *verifyRuntime, *Receipt) {
	t.Helper()
	p, _, _, r := verifyFixture(t)
	receipt, err := Verify(context.Background(), r, p)
	if err != nil {
		t.Fatal(err)
	}
	r.patchResult = cloneObject(p.candidate)
	return p, r, receipt
}

func TestApplyConsumesReceiptAndPatchesApprovedPaths(t *testing.T) {
	p, r, receipt := authorizedRuntime(t)
	r.afterPatch = func() { r.workload = cloneObject(p.candidate) }
	applied, out, err := Apply(context.Background(), r, p, receipt)
	if err != nil {
		t.Fatal(err)
	}
	if applied == nil || out.Partial || r.writes != 1 {
		t.Fatalf("applied=%v outcome=%+v writes=%d", applied, out, r.writes)
	}
	if err := receipt.consume(); err == nil {
		t.Fatal("apply did not consume receipt")
	}
}

func TestApplyLostResponseIsNotRetried(t *testing.T) {
	p, r, receipt := authorizedRuntime(t)
	r.patchErr = errors.New("connection reset after write")
	r.afterPatch = func() { r.workload = cloneObject(p.candidate) }
	applied, out, err := Apply(context.Background(), r, p, receipt)
	if err == nil || applied == nil || !out.Partial || r.writes != 1 {
		t.Fatalf("lost write classification applied=%v outcome=%+v err=%v writes=%d", applied, out, err, r.writes)
	}
}

func TestObserveSeparatesRolloutFromHPAHealth(t *testing.T) {
	p, r, receipt := authorizedRuntime(t)
	r.afterPatch = func() { r.workload = cloneObject(p.candidate) }
	applied, _, err := Apply(context.Background(), r, p, receipt)
	if err != nil {
		t.Fatal(err)
	}
	r.hpa.Status.Conditions = []autoscalingv2.HorizontalPodAutoscalerCondition{{Type: autoscalingv2.AbleToScale, Status: corev1.ConditionTrue}, {Type: autoscalingv2.ScalingActive, Status: corev1.ConditionFalse, Reason: "FailedGetResourceMetric"}}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	out, err := Observe(ctx, r, applied)
	if err != nil {
		t.Fatal(err)
	}
	if !out.WorkloadConverged || out.HPAHealthy {
		t.Fatalf("workload and HPA results collapsed: %+v", out)
	}
}

func TestObserveDoesNotClaimConvergenceAfterApprovedRequestIsReverted(t *testing.T) {
	p, r, receipt := authorizedRuntime(t)
	r.afterPatch = func() { r.workload = cloneObject(p.candidate) }
	applied, _, err := Apply(context.Background(), r, p, receipt)
	if err != nil {
		t.Fatal(err)
	}
	r.workload = cloneObject(p.original)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	out, err := Observe(ctx, r, applied)
	if err != nil {
		t.Fatal(err)
	}
	if out.WorkloadConverged || !out.Partial {
		t.Fatalf("reverted approved request reported converged: %+v", out)
	}
}

func TestObserveTreatsScalingLimitAsPartial(t *testing.T) {
	p, r, receipt := authorizedRuntime(t)
	r.afterPatch = func() { r.workload = cloneObject(p.candidate) }
	applied, _, err := Apply(context.Background(), r, p, receipt)
	if err != nil {
		t.Fatal(err)
	}
	r.hpa.Status.Conditions = append(r.hpa.Status.Conditions, autoscalingv2.HorizontalPodAutoscalerCondition{Type: autoscalingv2.ScalingLimited, Status: corev1.ConditionTrue, Reason: "TooManyReplicas"})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	out, err := Observe(ctx, r, applied)
	if err != nil {
		t.Fatal(err)
	}
	if out.HPAHealthy || !out.Partial {
		t.Fatalf("scaling limit reported healthy: %+v", out)
	}
}

func TestRollbackRestoresOnlyAddedRequests(t *testing.T) {
	p, r, receipt := authorizedRuntime(t)
	r.afterPatch = func() { r.workload = cloneObject(p.candidate) }
	applied, _, err := Apply(context.Background(), r, p, receipt)
	if err != nil {
		t.Fatal(err)
	}
	nestedMap(r.workload, "metadata")["annotations"] = map[string]any{"operator": "keep"}
	r.afterPatch = func() {
		resources := nestedMap(r.workload, "spec", "template", "spec")["containers"].([]any)[0].(map[string]any)["resources"].(map[string]any)
		delete(resources["requests"].(map[string]any), "memory")
		r.patchResult = cloneObject(r.workload)
	}
	out, err := Rollback(context.Background(), r, applied)
	if err != nil {
		t.Fatal(err)
	}
	if out.Partial || nestedMap(r.workload, "metadata", "annotations")["operator"] != "keep" {
		t.Fatalf("rollback damaged unrelated fields: %+v %#v", out, r.workload)
	}
	requests := nestedMap(r.workload, "spec", "template", "spec")["containers"].([]any)[0].(map[string]any)["resources"].(map[string]any)["requests"].(map[string]any)
	if _, ok := requests["memory"]; ok {
		t.Fatalf("added request remains: %#v", requests)
	}
	if requests["cpu"] != "100m" {
		t.Fatalf("existing request changed: %#v", requests)
	}
}

func TestRollbackFindsTargetByNameAfterUnrelatedContainerInsertion(t *testing.T) {
	p, r, receipt := authorizedRuntime(t)
	r.afterPatch = func() { r.workload = cloneObject(p.candidate) }
	applied, _, err := Apply(context.Background(), r, p, receipt)
	if err != nil {
		t.Fatal(err)
	}
	containers := nestedMap(r.workload, "spec", "template", "spec")["containers"].([]any)
	nestedMap(r.workload, "spec", "template", "spec")["containers"] = append([]any{map[string]any{"name": "sidecar", "image": "example/sidecar"}}, containers...)
	r.afterPatch = func() {
		requests := nestedMap(r.workload, "spec", "template", "spec")["containers"].([]any)[1].(map[string]any)["resources"].(map[string]any)["requests"].(map[string]any)
		delete(requests, "memory")
		r.patchResult = cloneObject(r.workload)
	}
	out, err := Rollback(context.Background(), r, applied)
	if err != nil {
		t.Fatal(err)
	}
	containers = nestedMap(r.workload, "spec", "template", "spec")["containers"].([]any)
	if !out.RolledBack || containers[0].(map[string]any)["name"] != "sidecar" {
		t.Fatalf("rollback damaged unrelated container ordering: %+v %#v", out, containers)
	}
	requests := nestedMap(containers[1].(map[string]any), "resources", "requests")
	if _, ok := requests["memory"]; ok {
		t.Fatalf("Fixora-added memory request remains: %#v", requests)
	}
}

func TestRollbackRejectsResponseThatRetainsApprovedRequest(t *testing.T) {
	p, r, receipt := authorizedRuntime(t)
	r.afterPatch = func() { r.workload = cloneObject(p.candidate) }
	applied, _, err := Apply(context.Background(), r, p, receipt)
	if err != nil {
		t.Fatal(err)
	}
	r.patchResult = cloneObject(p.candidate)
	if out, err := Rollback(context.Background(), r, applied); err == nil || out.RolledBack {
		t.Fatalf("rollback response retaining approved request was accepted: %+v err=%v", out, err)
	}
}

func TestRollbackRejectsChangedRequestOrUID(t *testing.T) {
	t.Run("request", func(t *testing.T) {
		p, r, receipt := authorizedRuntime(t)
		r.afterPatch = func() { r.workload = cloneObject(p.candidate) }
		applied, _, err := Apply(context.Background(), r, p, receipt)
		if err != nil {
			t.Fatal(err)
		}
		nestedMap(r.workload, "spec", "template", "spec")["containers"].([]any)[0].(map[string]any)["resources"].(map[string]any)["requests"].(map[string]any)["memory"] = "256Mi"
		if _, err := Rollback(context.Background(), r, applied); err == nil {
			t.Fatal("changed approved request was removed")
		}
	})
	t.Run("uid", func(t *testing.T) {
		p, r, receipt := authorizedRuntime(t)
		r.afterPatch = func() { r.workload = cloneObject(p.candidate) }
		applied, _, err := Apply(context.Background(), r, p, receipt)
		if err != nil {
			t.Fatal(err)
		}
		nestedMap(r.workload, "metadata")["uid"] = "replacement"
		if _, err := Rollback(context.Background(), r, applied); err == nil {
			t.Fatal("replacement workload was modified")
		}
	})
}
