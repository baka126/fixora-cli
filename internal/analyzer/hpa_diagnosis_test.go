package analyzer

import (
	"context"
	"errors"
	"testing"
)

func hpaDiagnosticFixture(metricType string, condition map[string]any) (map[string]any, map[string]any) {
	metric := map[string]any{"type": "Resource", "resource": map[string]any{"name": "cpu", "target": map[string]any{"type": metricType, "averageUtilization": int64(70)}}}
	hpa := map[string]any{
		"apiVersion": "autoscaling/v2",
		"metadata":   map[string]any{"name": "api-scale", "namespace": "prod", "generation": int64(5), "uid": "hpa-uid"},
		"spec":       map[string]any{"scaleTargetRef": map[string]any{"apiVersion": "apps/v1", "kind": "Deployment", "name": "api"}, "metrics": []any{metric}, "maxReplicas": int64(5)},
		"status":     map[string]any{"observedGeneration": int64(5), "currentReplicas": int64(2), "desiredReplicas": int64(2), "currentMetrics": []any{map[string]any{"type": "Resource", "resource": map[string]any{"name": "cpu", "current": map[string]any{"averageUtilization": int64(50)}}}}},
	}
	if condition != nil {
		hpa["status"].(map[string]any)["conditions"] = []any{condition}
	}
	workload := map[string]any{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": map[string]any{"name": "api", "namespace": "prod"}, "spec": map[string]any{"template": map[string]any{"spec": map[string]any{"containers": []any{map[string]any{"name": "server", "resources": map[string]any{"requests": map[string]any{"cpu": "100m"}}}}}}}}
	return hpa, workload
}

func runHPADiagnostics(t *testing.T, hpa, workload map[string]any) ([]Finding, error) {
	t.Helper()
	r := fakeReader{items: map[string][]map[string]any{"hpa": {hpa}}, resource: workload}
	return New(r, Options{Namespace: "prod"}).analyzeHPATargets(NewScanContext(context.Background(), r, Options{Namespace: "prod"}))
}

func TestHPAObservedGenerationStale(t *testing.T) {
	hpa, workload := hpaDiagnosticFixture("Utilization", map[string]any{"type": "ScalingActive", "status": "True", "reason": "ValidMetricFound"})
	hpa["status"].(map[string]any)["observedGeneration"] = int64(4)
	findings, err := runHPADiagnostics(t, hpa, workload)
	if err != nil {
		t.Fatal(err)
	}
	for _, finding := range findings {
		if finding.Status == "HPAStatusStale" {
			return
		}
	}
	t.Fatalf("stale HPA generation was not diagnosed: %#v", findings)
}

func TestAnalyzeResourceUsesCanonicalHPAKindForAlias(t *testing.T) {
	hpa, _ := hpaDiagnosticFixture("Utilization", nil)
	r := fakeReader{resource: hpa}
	finding, err := New(r, Options{Namespace: "prod"}).AnalyzeResource(context.Background(), "hpa/api-scale")
	if err != nil {
		t.Fatal(err)
	}
	if finding.ResourceKind != "HorizontalPodAutoscaler" {
		t.Fatalf("HPA alias produced noncanonical resource kind %q", finding.ResourceKind)
	}
}

func TestHPAMetricsConditionsRemainDistinct(t *testing.T) {
	cases := []struct {
		name, reason, want string
	}{
		{"metrics unavailable", "FailedGetResourceMetric", "MetricsUnavailable"},
		{"invalid metric target", "InvalidMetricSource", "MetricTargetInvalid"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hpa, workload := hpaDiagnosticFixture("Utilization", map[string]any{"type": "ScalingActive", "status": "False", "reason": tc.reason, "message": "autoscaler could not use the configured metric"})
			findings, err := runHPADiagnostics(t, hpa, workload)
			if err != nil {
				t.Fatal(err)
			}
			for _, finding := range findings {
				if finding.Status == tc.want && finding.ResourceName == "api-scale" {
					return
				}
			}
			t.Fatalf("condition reason %q did not produce %q: %#v", tc.reason, tc.want, findings)
		})
	}
}

func TestHPAMissingRequestsOnlyForUtilization(t *testing.T) {
	hpa, workload := hpaDiagnosticFixture("AverageValue", nil)
	workload["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)["containers"].([]any)[0].(map[string]any)["resources"] = map[string]any{}
	findings, err := runHPADiagnostics(t, hpa, workload)
	if err != nil {
		t.Fatal(err)
	}
	for _, finding := range findings {
		if finding.Status == "MissingResourceRequests" {
			t.Fatalf("AverageValue must not require requests for utilization calculation: %#v", finding)
		}
	}

	hpa, workload = hpaDiagnosticFixture("Utilization", nil)
	workload["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)["containers"].([]any)[0].(map[string]any)["resources"] = map[string]any{}
	findings, err = runHPADiagnostics(t, hpa, workload)
	if err != nil {
		t.Fatal(err)
	}
	for _, finding := range findings {
		if finding.Status == "MissingResourceRequests" {
			return
		}
	}
	t.Fatalf("Utilization metric missing request was not diagnosed: %#v", findings)
}

func TestHPAInvalidSpecsAndUnsupportedTargets(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(map[string]any)
		want   string
	}{
		{
			name: "no metric source",
			mutate: func(hpa map[string]any) {
				hpa["spec"].(map[string]any)["metrics"] = []any{}
			},
			want: "InvalidHPASpec",
		},
		{
			name: "unsupported target kind",
			mutate: func(hpa map[string]any) {
				hpa["spec"].(map[string]any)["scaleTargetRef"].(map[string]any)["kind"] = "CronJob"
			},
			want: "UnsupportedScaleTarget",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hpa, workload := hpaDiagnosticFixture("Utilization", nil)
			tc.mutate(hpa)
			findings, err := runHPADiagnostics(t, hpa, workload)
			if err != nil {
				t.Fatal(err)
			}
			for _, finding := range findings {
				if finding.Status == tc.want {
					return
				}
			}
			t.Fatalf("expected %s for %s: %#v", tc.want, tc.name, findings)
		})
	}
}

func TestHPAReadFailuresRemainUnavailable(t *testing.T) {
	hpa, _ := hpaDiagnosticFixture("Utilization", map[string]any{"type": "ScalingActive", "status": "False", "reason": "FailedGetResourceMetric", "message": "metrics API forbidden"})
	r := fakeReader{itemErrs: map[string]error{"hpa": errors.New("horizontalpodautoscalers.autoscaling is forbidden")}}
	findings, err := New(r, Options{Namespace: "prod"}).analyzeHPATargets(NewScanContext(context.Background(), r, Options{Namespace: "prod"}))
	if err == nil || findings != nil {
		t.Fatalf("unreadable HPA inventory must remain an error, findings=%#v err=%v", findings, err)
	}

	_, workload := hpaDiagnosticFixture("Utilization", nil)
	reader := hpaTargetErrorReader{fakeReader: fakeReader{items: map[string][]map[string]any{"hpa": {hpa}}, resource: workload}, targetErr: errors.New("deployments.apps is forbidden")}
	findings, err = New(reader, Options{Namespace: "prod"}).analyzeHPATargets(NewScanContext(context.Background(), reader, Options{Namespace: "prod"}))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"ScaleTargetUnreadable": false, "MetricsUnavailable": false}
	for _, finding := range findings {
		if _, ok := want[finding.Status]; ok {
			want[finding.Status] = true
		}
		if finding.Status == "MissingScaleTarget" {
			t.Fatalf("forbidden target read must not be treated as missing: %#v", finding)
		}
	}
	for status, found := range want {
		if !found {
			t.Fatalf("missing %s diagnosis: %#v", status, findings)
		}
	}
}
