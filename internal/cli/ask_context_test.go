package cli

import (
	"fmt"
	"strings"
	"testing"

	"github.com/fixora/kubectl-fixora/internal/analyzer"
)

func TestAskContextPoliciesCoverAnalyzerRegistry(t *testing.T) {
	definitions := analyzer.ListAnalyzers(nil)
	seen := map[string]bool{}
	for _, definition := range definitions {
		if seen[definition.Kind] {
			t.Fatalf("analyzer registry contains duplicate kind %q", definition.Kind)
		}
		seen[definition.Kind] = true
		if _, ok := askContextPolicyForKind(definition.Kind); !ok {
			t.Errorf("registered kind %q has no Ask context policy", definition.Kind)
		}
	}
	if len(askContextPolicies) != len(definitions) {
		t.Fatalf("context policies=%d registry definitions=%d; policies must map one-to-one", len(askContextPolicies), len(definitions))
	}
	if _, ok := askContextPolicyForKind("UnregisteredCustomResource"); ok {
		t.Fatal("unregistered custom resource unexpectedly received a context policy")
	}
}

func TestAskObjectContextEvidenceIncludesOnlyCommonSafeStatus(t *testing.T) {
	object := map[string]any{
		"kind": "Deployment",
		"status": map[string]any{
			"phase":             "Running",
			"readyReplicas":     int64(2),
			"availableReplicas": int64(1),
			"conditions": []any{map[string]any{
				"type": "Available", "status": "False", "reason": "MinimumReplicasUnavailable",
				"message": "message-secret", "operatorNote": "arbitrary-status-secret",
			}},
			"privateStatus": "must-not-be-serialized",
		},
		"metadata": map[string]any{"annotations": map[string]any{"note": "annotation-secret"}},
	}

	evidence := askObjectContextEvidence("Deployment", object, false)
	values := map[string]string{}
	var all strings.Builder
	for _, item := range evidence {
		values[item.Label] = item.Value
		all.WriteString(item.Label)
		all.WriteByte(':')
		all.WriteString(item.Value)
		all.WriteByte('\n')
	}
	for label, want := range map[string]string{
		"Phase":              "Running",
		"Ready replicas":     "2",
		"Available replicas": "1",
		"Condition states":   "Available=False:MinimumReplicasUnavailable",
	} {
		if got := values[label]; got != want {
			t.Errorf("evidence %q=%q, want %q (all evidence: %s)", label, got, want, all.String())
		}
	}
	for _, excluded := range []string{"message-secret", "arbitrary-status-secret", "must-not-be-serialized", "annotation-secret"} {
		if strings.Contains(all.String(), excluded) {
			t.Errorf("evidence contains excluded status/metadata value %q: %s", excluded, all.String())
		}
	}
	if len(evidence) > 12 {
		t.Fatalf("common context evidence has %d entries; want at most 12", len(evidence))
	}
	for _, item := range evidence {
		if len(item.Value) > 512 {
			t.Errorf("evidence value %q exceeds 512 bytes", fmt.Sprint(item.Label))
		}
	}
}

func TestAskContextBuiltInKinds(t *testing.T) {
	workloadSpec := func() map[string]any {
		return map[string]any{"containers": []any{map[string]any{
			"name": "api", "image": "example/api:v1",
			"ports":          []any{map[string]any{"name": "http", "containerPort": int64(8080), "protocol": "TCP"}},
			"readinessProbe": map[string]any{"httpGet": map[string]any{"path": "/ready", "port": int64(8080)}},
			"resources":      map[string]any{"requests": map[string]any{"cpu": "100m"}},
		}}}
	}
	workloadObject := func(templatePath []string, status map[string]any) map[string]any {
		podSpec := workloadSpec()
		var template any = map[string]any{"spec": podSpec}
		if len(templatePath) == 2 && templatePath[0] == "jobTemplate" {
			template = map[string]any{"spec": map[string]any{"template": map[string]any{"spec": podSpec}}}
		}
		spec := map[string]any{"template": template}
		if len(templatePath) == 1 && templatePath[0] == "template" {
			spec["template"] = template
		}
		if status == nil {
			status = map[string]any{}
		}
		return map[string]any{"spec": spec, "status": status}
	}
	baseRefs := []any{map[string]any{"name": "api", "port": int64(8080)}}
	parentRefs := []any{map[string]any{"name": "edge", "namespace": "payments"}}
	networkPolicyObject := map[string]any{
		"spec": map[string]any{
			"podSelector": map[string]any{"matchLabels": map[string]any{"app": "api"}},
			"policyTypes": []any{"Ingress", "Egress"},
			"ingress":     []any{map[string]any{"from": []any{map[string]any{"podSelector": map[string]any{}}}}},
		},
	}
	pvcObject := map[string]any{
		"spec": map[string]any{
			"storageClassName": "fast",
			"accessModes":      []any{"ReadWriteOnce"},
			"resources":        map[string]any{"requests": map[string]any{"storage": "10Gi"}},
		},
		"status": map[string]any{"phase": "Bound", "capacity": map[string]any{"storage": "10Gi"}},
	}
	tests := []struct {
		kind      string
		object    map[string]any
		wantLabel string
		want      string
	}{
		{
			kind: "Pod",
			object: map[string]any{
				"spec": workloadSpec(),
				"status": map[string]any{
					"phase":             "Running",
					"containerStatuses": []any{map[string]any{"name": "api", "ready": true, "restartCount": int64(0), "state": map[string]any{"running": map[string]any{}}}},
				},
			},
			wantLabel: "Container image", want: "api=example/api:v1",
		},
		{kind: "ReplicaSet", object: workloadObject([]string{"template"}, map[string]any{"readyReplicas": int64(1)}), wantLabel: "Container ports", want: "api=http:8080/TCP"},
		{kind: "Deployment", object: workloadObject([]string{"template"}, map[string]any{"availableReplicas": int64(1)}), wantLabel: "Configured readiness probe", want: "api=httpGet"},
		{kind: "StatefulSet", object: workloadObject([]string{"template"}, map[string]any{"readyReplicas": int64(1), "currentReplicas": int64(2), "updatedReplicas": int64(1)}), wantLabel: "StatefulSet replicas", want: "current=2 updated=1 ready=1"},
		{kind: "DaemonSet", object: workloadObject([]string{"template"}, map[string]any{"desiredNumberScheduled": int64(3), "numberReady": int64(2)}), wantLabel: "DaemonSet scheduling", want: "desired=3 ready=2"},
		{kind: "Job", object: workloadObject([]string{"template"}, map[string]any{"active": int64(1), "succeeded": int64(2), "failed": int64(0)}), wantLabel: "Job progress", want: "active=1 succeeded=2 failed=0"},
		{kind: "CronJob", object: map[string]any{"spec": map[string]any{"schedule": "*/5 * * * *", "suspend": false}}, wantLabel: "CronJob schedule", want: "*/5 * * * *"},
		{kind: "Service", object: map[string]any{"spec": map[string]any{"type": "ClusterIP", "selector": map[string]any{"app": "api"}, "ports": []any{map[string]any{"port": int64(80), "targetPort": int64(8080)}}}}, wantLabel: "Service selector", want: "{\"app\":\"api\"}"},
		{kind: "Ingress", object: map[string]any{"spec": map[string]any{"rules": []any{map[string]any{"host": "api.example.test", "http": map[string]any{"paths": []any{map[string]any{"backend": map[string]any{"service": map[string]any{"name": "api", "port": map[string]any{"number": int64(80)}}}}}}}}}}, wantLabel: "Ingress backends", want: "api:80"},
		{kind: "GatewayClass", object: map[string]any{"spec": map[string]any{"controllerName": "example.net/gateway-controller"}}, wantLabel: "GatewayClass controller", want: "example.net/gateway-controller"},
		{kind: "Gateway", object: map[string]any{"spec": map[string]any{"listeners": []any{map[string]any{"name": "https", "protocol": "HTTPS", "port": int64(443), "hostname": "api.example.test", "tls": map[string]any{"certificateRefs": []any{map[string]any{"name": "private-cert"}}}}}}}, wantLabel: "Gateway listeners", want: "https=HTTPS:443:api.example.test"},
		{kind: "HTTPRoute", object: map[string]any{"spec": map[string]any{"parentRefs": parentRefs, "rules": []any{map[string]any{"backendRefs": baseRefs}}}}, wantLabel: "HTTPRoute parents", want: "payments/edge"},
		{kind: "NetworkPolicy", object: networkPolicyObject, wantLabel: "NetworkPolicy ingress rule count", want: "1"},
		{kind: "PersistentVolumeClaim", object: pvcObject, wantLabel: "PVC requested storage", want: "10Gi"},
		{kind: "StorageClass", object: map[string]any{"provisioner": "example.csi", "reclaimPolicy": "Delete", "volumeBindingMode": "WaitForFirstConsumer", "allowVolumeExpansion": true, "parameters": map[string]any{"token": "provider-secret"}}, wantLabel: "StorageClass provisioner", want: "example.csi"},
		{kind: "Node", object: map[string]any{"spec": map[string]any{"unschedulable": false, "taints": []any{map[string]any{"key": "dedicated", "effect": "NoSchedule"}}}, "status": map[string]any{"capacity": map[string]any{"cpu": "4", "memory": "8Gi", "pods": "110"}, "allocatable": map[string]any{"cpu": "3900m", "memory": "7Gi", "pods": "100"}}}, wantLabel: "Node allocatable", want: "cpu=3900m memory=7Gi pods=100"},
		{kind: "ResourceClaim", object: map[string]any{"spec": map[string]any{"resourceClassName": "gpu", "allocationMode": "ExactCount", "parametersRef": map[string]any{"name": "private-parameters"}}, "status": map[string]any{"allocation": map[string]any{"devices": map[string]any{"results": []any{map[string]any{"driver": "vendor.example", "pool": "pool-a", "device": "gpu-0"}}}}}}, wantLabel: "ResourceClaim resource class", want: "gpu"},
	}
	for _, test := range tests {
		t.Run(test.kind, func(t *testing.T) {
			if _, ok := askContextPolicyForKind(test.kind); !ok {
				t.Fatalf("kind %q has no policy", test.kind)
			}
			evidence := askObjectContextEvidence(test.kind, test.object, false)
			if got := askContextEvidenceValue(evidence, test.wantLabel); got != test.want {
				t.Fatalf("%s context=%q, want %q; evidence=%#v", test.wantLabel, got, test.want, evidence)
			}
			if len(evidence) > 12 {
				t.Fatalf("context contains %d evidence items; max is 12", len(evidence))
			}
			for _, item := range evidence {
				if len(item.Value) > 512 {
					t.Errorf("context value %q exceeds 512 bytes", item.Label)
				}
			}
		})
	}
}

func TestAskPodContextIncludesBoundedContainerFailureState(t *testing.T) {
	object := map[string]any{"status": map[string]any{"phase": "Running", "containerStatuses": []any{map[string]any{
		"name": "api", "ready": false, "restartCount": int64(4),
		"state": map[string]any{"terminated": map[string]any{"reason": "Error", "exitCode": int64(1), "message": "private-status-message"}},
	}}}}
	evidence := askObjectContextEvidence("Pod", object, false)
	if got := askContextEvidenceValue(evidence, "Container status"); got != "api ready=false restarts=4 state=terminated:Error:exit=1" {
		t.Fatalf("Pod container status=%q; evidence=%#v", got, evidence)
	}
	for _, item := range evidence {
		if strings.Contains(item.Value, "private-status-message") {
			t.Fatalf("Pod status message leaked into context: %#v", evidence)
		}
	}
}

func TestAskContextExcludesOpaqueFields(t *testing.T) {
	object := map[string]any{
		"spec":     map[string]any{"controllerName": "example.net/controller", "parameters": map[string]any{"token": "provider-secret"}},
		"metadata": map[string]any{"annotations": map[string]any{"note": "annotation-secret"}},
	}
	evidence := askObjectContextEvidence("GatewayClass", object, false)
	for _, item := range evidence {
		if strings.Contains(item.Value, "provider-secret") || strings.Contains(item.Value, "annotation-secret") {
			t.Fatalf("context exposed opaque or annotated data: %#v", evidence)
		}
	}
}

func TestAskContextOptionalAndPolicyKinds(t *testing.T) {
	tests := []struct {
		kind      string
		object    map[string]any
		wantLabel string
		want      string
	}{
		{"HorizontalPodAutoscaler", map[string]any{"spec": map[string]any{"scaleTargetRef": map[string]any{"kind": "Deployment", "name": "api"}, "minReplicas": int64(2), "maxReplicas": int64(8)}, "status": map[string]any{"currentReplicas": int64(3)}}, "HPA scale bounds", "min=2 max=8"},
		{"PodDisruptionBudget", map[string]any{"spec": map[string]any{"minAvailable": "75%", "maxUnavailable": int64(1), "selector": map[string]any{"matchLabels": map[string]any{"app": "api"}}}, "status": map[string]any{"disruptionsAllowed": int64(0), "currentHealthy": int64(2)}}, "PDB disruption state", "allowed=0 healthy=2"},
		{"ScaledObject", map[string]any{"spec": map[string]any{"scaleTargetRef": map[string]any{"name": "worker"}, "minReplicaCount": int64(1), "maxReplicaCount": int64(10)}, "status": map[string]any{"conditions": []any{map[string]any{"type": "Ready", "status": "True"}}}}, "KEDA scale bounds", "min=1 max=10"},
		{"MutatingWebhookConfiguration", map[string]any{"webhooks": []any{map[string]any{"name": "mutate.example.test", "sideEffects": "None", "clientConfig": map[string]any{"service": map[string]any{"name": "hook", "namespace": "system"}, "caBundle": "CA-SENTINEL"}, "rules": []any{map[string]any{"apiGroups": []any{"apps"}, "resources": []any{"deployments"}}}}}}, "Webhook names", "mutate.example.test"},
		{"ValidatingWebhookConfiguration", map[string]any{"webhooks": []any{map[string]any{"name": "validate.example.test", "failurePolicy": "Fail", "clientConfig": map[string]any{"url": "https://hook.example.test", "caBundle": "CA-SENTINEL"}}}}, "Webhook names", "validate.example.test"},
		{"PolicyReport", map[string]any{"summary": map[string]any{"pass": int64(2), "fail": int64(1), "warn": int64(0), "error": int64(0)}, "results": []any{map[string]any{"message": "private-report-message"}}}, "Policy report summary", "pass=2 fail=1 warn=0 error=0"},
		{"VulnerabilityReport", map[string]any{"report": map[string]any{"artifact": map[string]any{"repository": "example/api", "tag": "v1"}, "summary": map[string]any{"criticalCount": int64(1), "highCount": int64(2)}, "scanner": map[string]any{"parameters": map[string]any{"token": "provider-secret"}}}}, "Vulnerability summary", "critical=1 high=2"},
		{"ConfigAuditReport", map[string]any{"report": map[string]any{"summary": map[string]any{"criticalCount": int64(0), "highCount": int64(1), "mediumCount": int64(2)}}}, "Audit summary", "critical=0 high=1 medium=2"},
		{"ClusterServiceVersion", map[string]any{"spec": map[string]any{"version": "1.2.3", "install": map[string]any{"strategy": "deployment", "spec": map[string]any{"secret": "private"}}}, "status": map[string]any{"phase": "Succeeded", "reason": "InstallSucceeded"}}, "Operator phase", "Succeeded"},
		{"Subscription", map[string]any{"spec": map[string]any{"channel": "stable", "source": "catalog", "sourceNamespace": "operators"}, "status": map[string]any{"installedCSV": "operator.v1.2.3", "currentCSV": "operator.v1.2.3", "state": "AtLatest"}}, "Subscription state", "AtLatest"},
		{"InstallPlan", map[string]any{"spec": map[string]any{"approved": true, "approval": "Manual"}, "status": map[string]any{"phase": "Complete", "plan": []any{map[string]any{"resource": "secret"}}}}, "InstallPlan phase", "Complete"},
		{"CatalogSource", map[string]any{"spec": map[string]any{"sourceType": "grpc", "image": "example/catalog:v1", "secrets": []any{"registry-secret"}}, "status": map[string]any{"connectionState": map[string]any{"lastObservedState": "READY"}}}, "Catalog connection state", "READY"},
		{"OperatorGroup", map[string]any{"spec": map[string]any{"targetNamespaces": []any{"operators", "apps"}, "staticProvidedAPIs": []any{"Secret.v1"}}}, "OperatorGroup target namespace count", "2"},
		{"ClusterCatalog", map[string]any{"spec": map[string]any{"source": map[string]any{"type": "Image", "image": "example/catalog:v1", "secretRef": "registry-secret"}}, "status": map[string]any{"conditions": []any{map[string]any{"type": "Accepted", "status": "True"}}}}, "ClusterCatalog source type", "Image"},
		{"ClusterExtension", map[string]any{"spec": map[string]any{"source": map[string]any{"sourceType": "Catalog", "name": "community"}, "namespace": "operators", "serviceAccount": map[string]any{"name": "operator-sa"}}, "status": map[string]any{"state": "Succeeded", "conditions": []any{map[string]any{"type": "Installed", "status": "True"}}}}, "ClusterExtension state", "Succeeded"},
		{"ConfigMap", map[string]any{"data": map[string]any{"APP_MODE": "private-config-value", "ENDPOINT": "private-endpoint"}, "binaryData": map[string]any{"cert": "private-binary"}}, "ConfigMap keys", "APP_MODE,ENDPOINT,cert"},
	}
	for _, test := range tests {
		t.Run(test.kind, func(t *testing.T) {
			evidence := askObjectContextEvidence(test.kind, test.object, false)
			if got := askContextEvidenceValue(evidence, test.wantLabel); got != test.want {
				t.Fatalf("%s=%q, want %q; evidence=%#v", test.wantLabel, got, test.want, evidence)
			}
			var serialized strings.Builder
			for _, item := range evidence {
				serialized.WriteString(item.Label + ":" + item.Value + "\n")
			}
			for _, forbidden := range []string{"CA-SENTINEL", "private-report-message", "provider-secret", "private-config-value", "private-binary", "private"} {
				if strings.Contains(serialized.String(), forbidden) {
					t.Errorf("context exposed opaque payload %q: %s", forbidden, serialized.String())
				}
			}
		})
	}
}

func TestAskContextNeverIncludesSensitiveValues(t *testing.T) {
	secret := map[string]any{"type": "Opaque", "data": map[string]any{"password": "secret-value-sentinel"}, "stringData": map[string]any{"token": "string-data-sentinel"}}
	for _, checkKeys := range []bool{false, true} {
		evidence := askObjectContextEvidence("Secret", secret, checkKeys)
		var serialized strings.Builder
		for _, item := range evidence {
			serialized.WriteString(item.Label + ":" + item.Value + "\n")
		}
		output := serialized.String()
		for _, forbidden := range []string{"secret-value-sentinel", "string-data-sentinel"} {
			if strings.Contains(output, forbidden) {
				t.Fatalf("secret values leaked when checkSecretKeys=%v: %s", checkKeys, output)
			}
		}
		if checkKeys {
			if got := askContextEvidenceValue(evidence, "Secret keys"); got != "password,token" {
				t.Fatalf("opt-in Secret keys=%q, want key names", got)
			}
		} else if strings.Contains(output, "Secret keys") || strings.Contains(output, "Secret type") {
			t.Fatalf("Secret context present without opt-in: %s", output)
		}
	}
}

func TestAskContextSkipsUnexpectedStructuredValuesAtScalarPaths(t *testing.T) {
	tests := []struct {
		kind   string
		object map[string]any
	}{
		{"VulnerabilityReport", map[string]any{"report": map[string]any{"summary": map[string]any{"criticalCount": map[string]any{"opaque": "nested-report-sentinel"}}}}},
		{"Gateway", map[string]any{"spec": map[string]any{"listeners": []any{map[string]any{"name": "https", "protocol": "HTTPS", "port": map[string]any{"opaque": "nested-port-sentinel"}}}}}},
		{"HorizontalPodAutoscaler", map[string]any{"spec": map[string]any{"maxReplicas": map[string]any{"opaque": "nested-replica-sentinel"}}}},
	}
	for _, test := range tests {
		t.Run(test.kind, func(t *testing.T) {
			for _, evidence := range askObjectContextEvidence(test.kind, test.object, false) {
				if strings.Contains(evidence.Value, "nested-") || strings.Contains(evidence.Label, "summary") {
					t.Fatalf("unexpected nested value reached evidence: %#v", evidence)
				}
			}
		})
	}
}

func askContextEvidenceValue(evidence []analyzer.Evidence, label string) string {
	for _, item := range evidence {
		if item.Label == label {
			return item.Value
		}
	}
	return ""
}
