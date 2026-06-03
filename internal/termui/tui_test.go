package termui

import (
	"testing"

	"github.com/charmbracelet/bubbles/table"
	"github.com/fixora/kubectl-fixora/internal/analyzer"
)

func TestTUIRowsFilterAndSelect(t *testing.T) {
	m := tuiModel{
		table: table.New(table.WithColumns([]table.Column{
			{Title: "SEV", Width: 8},
			{Title: "NS", Width: 12},
			{Title: "RESOURCE", Width: 28},
			{Title: "STATUS", Width: 20},
		})),
		report: analyzer.ScanReport{Findings: []analyzer.Finding{
			{ID: "low", Namespace: "dev", ResourceKind: "Deployment", ResourceName: "worker", Severity: "low", Status: "Pending", Summary: "waiting"},
			{ID: "high", Namespace: "prod", ResourceKind: "Deployment", ResourceName: "api", Severity: "high", Status: "CrashLoopBackOff", Summary: "panic"},
		}},
		filter: "api",
	}

	m.updateRows()
	m.syncSelected()

	if got := len(m.table.Rows()); got != 1 {
		t.Fatalf("rows = %d, want 1", got)
	}
	if m.selected.ID != "high" {
		t.Fatalf("selected = %q, want high", m.selected.ID)
	}
}

func TestContainsAny(t *testing.T) {
	if !containsAny("Service has no Endpoints", "endpoint") {
		t.Fatal("expected case-insensitive match")
	}
	if containsAny("Deployment crashed", "storage", "rbac") {
		t.Fatal("did not expect unrelated term match")
	}
}

func TestHelpTextIncludesShadowVerify(t *testing.T) {
	if !containsAny(helpText(), "shadow verify") {
		t.Fatal("expected TUI help to mention shadow verify")
	}
	if !containsAny(helpText(), "ai root cause") {
		t.Fatal("expected TUI help to mention AI root cause")
	}
	if !containsAny(helpText(), "github pr", "gitlab mr") {
		t.Fatal("expected TUI help to mention PR/MR delivery")
	}
}
