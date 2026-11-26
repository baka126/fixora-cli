package analyzer

import (
	"context"
	"testing"
)

func TestIngressLegacyClassAnnotationAvoidsMissingClassFinding(t *testing.T) {
	ingress := map[string]any{
		"metadata": map[string]any{"name": "web", "namespace": "prod", "annotations": map[string]any{"kubernetes.io/ingress.class": "alb"}},
		"spec":     map[string]any{},
	}
	reader := fakeReader{items: map[string][]map[string]any{"ingresses": {ingress}}}
	ctx := NewScanContext(context.Background(), reader, Options{Namespace: "prod"})
	findings, err := New(reader, Options{Namespace: "prod"}).analyzeIngressBackends(ctx)
	if err != nil || hasStatus(findings, "MissingIngressClass") != nil {
		t.Fatalf("legacy class reported missing: findings=%#v err=%v", findings, err)
	}
}

func TestIngressDefaultClassAvoidsMissingClassFinding(t *testing.T) {
	ingress := map[string]any{"metadata": map[string]any{"name": "web", "namespace": "prod"}, "spec": map[string]any{}}
	defaultClass := map[string]any{"metadata": map[string]any{"name": "nginx", "annotations": map[string]any{"ingressclass.kubernetes.io/is-default-class": "true"}}}
	reader := fakeReader{items: map[string][]map[string]any{"ingresses": {ingress}, "ingressclasses": {defaultClass}}}
	ctx := NewScanContext(context.Background(), reader, Options{Namespace: "prod"})
	findings, err := New(reader, Options{Namespace: "prod"}).analyzeIngressBackends(ctx)
	if err != nil || hasStatus(findings, "MissingIngressClass") != nil {
		t.Fatalf("default class reported missing: findings=%#v err=%v", findings, err)
	}
}
