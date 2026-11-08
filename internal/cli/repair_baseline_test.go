package cli

import (
	"context"
	"strings"
	"testing"
)

func TestRepairBaselineRequiresIdentityAndExplicitSelection(t *testing.T) {
	path, r, f, p := startupFixture(t, validStartupBaseline)
	if _, err := withRepairBaseline(context.Background(), r, options{repairBaseline: path, repairPolicy: "unknown"}, f, p); err == nil {
		t.Fatal("unknown policy accepted")
	}
	if _, err := withRepairBaseline(context.Background(), r, options{repairBaseline: path, startupBaseline: path}, f, p); err == nil {
		t.Fatal("mixed baseline authority accepted")
	}
	if _, err := withRepairBaseline(context.Background(), r, options{repairBaseline: path}, f, p); err == nil {
		t.Fatal("baseline without policy changes accepted")
	}
	both := strings.Replace(validStartupBaseline, "      containers:", "      nodeSelector: {pool: approved}\n      securityContext: {runAsNonRoot: true}\n      containers:", 1)
	path, r, f, p = startupFixture(t, both)
	if _, err := withRepairBaseline(context.Background(), r, options{repairBaseline: path}, f, p); err == nil || !strings.Contains(err.Error(), "select") {
		t.Fatalf("ambiguous baseline must request selection: %v", err)
	}
}
