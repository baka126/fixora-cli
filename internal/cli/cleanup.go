package cli

import (
	"context"
	"fmt"
	"io"

	"github.com/fixora/kubectl-fixora/internal/kube"
	"github.com/fixora/kubectl-fixora/internal/output"
	"github.com/fixora/kubectl-fixora/internal/shadow"
)

type cleanupReport struct {
	Status        string                    `json:"status"`
	Candidates    []shadow.CleanupCandidate `json:"candidates,omitempty"`
	Deleted       []string                  `json:"deleted,omitempty"`
	Failures      []string                  `json:"failures,omitempty"`
	NoChangesMade bool                      `json:"noChangesMade"`
}

func runCleanupCommand(ctx context.Context, stdout, stderr io.Writer, opts options) int {
	client, err := kube.NewRequiredTypedClient(opts.context, "Fixora shadow cleanup")
	if err != nil {
		fmt.Fprintf(stderr, "error: typed Kubernetes client: %v\n", err)
		return 1
	}
	return runCleanup(ctx, stdout, stderr, opts, client)
}

func runCleanup(ctx context.Context, stdout, stderr io.Writer, opts options, client *kube.TypedClient) int {
	candidates, err := shadow.ListCleanupCandidates(ctx, client)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	if !opts.yes {
		report := cleanupReport{Status: "preview", Candidates: candidates, NoChangesMade: true}
		if opts.output != "text" {
			return output.Write(stdout, opts.output, report)
		}
		if len(candidates) == 0 {
			fmt.Fprintln(stdout, "No Fixora shadow run namespaces found.")
			return 0
		}
		fmt.Fprintf(stdout, "Fixora shadow cleanup preview: %d namespace(s)\n", len(candidates))
		for _, candidate := range candidates {
			fmt.Fprintf(stdout, "  %s (source namespace: %s)\n", candidate.Namespace, candidate.SourceNamespace)
		}
		fmt.Fprintln(stdout, "Preview only; no changes made. Re-run with --yes to delete these isolated Fixora namespaces.")
		return 0
	}

	report := cleanupReport{Status: "empty", NoChangesMade: true}
	for _, candidate := range candidates {
		if err := shadow.DeleteCleanupCandidate(ctx, client, candidate); err != nil {
			report.Failures = append(report.Failures, fmt.Sprintf("%s: %v", candidate.Namespace, err))
			continue
		}
		report.Deleted = append(report.Deleted, candidate.Namespace)
	}
	if len(report.Deleted) > 0 {
		report.NoChangesMade = false
		report.Status = "deleted"
	}
	if len(report.Failures) > 0 {
		report.Status = "partial"
	}
	if opts.output != "text" {
		code := output.Write(stdout, opts.output, report)
		if len(report.Failures) > 0 {
			return 1
		}
		return code
	}
	if len(report.Deleted) == 0 && len(report.Failures) == 0 {
		fmt.Fprintln(stdout, "No Fixora shadow run namespaces found.")
		return 0
	}
	for _, name := range report.Deleted {
		fmt.Fprintf(stdout, "Deleted namespace/%s.\n", name)
	}
	for _, failure := range report.Failures {
		fmt.Fprintf(stderr, "cleanup failed for %s\n", failure)
	}
	if len(report.Failures) > 0 {
		return 1
	}
	return 0
}
