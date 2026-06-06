package cli

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/fixora/kubectl-fixora/internal/analyzer"
)

func promptIncidentSelection(ctx context.Context, a analyzer.Analyzer, stdout, stderr io.Writer, wide, noColor bool) (string, error) {
	fmt.Fprintln(stderr, "Scanning for incidents...")
	scan := a.ScanReport(ctx)
	if len(scan.Findings) == 0 {
		return "", fmt.Errorf("no active workload issues found")
	}

	for i, f := range scan.Findings {
		fmt.Fprintf(stdout, "[%d] %s/%s - %s: %s\n", i+1, f.ResourceKind, f.ResourceName, f.Status, f.Summary)
	}

	fmt.Fprint(stdout, "\nSelect a workload issue to debug (1-N): ")
	reader := bufio.NewReader(os.Stdin)
	response, err := reader.ReadString('\n')
	if err != nil {
		return "", err
	}

	response = strings.TrimSpace(response)
	idx, err := strconv.Atoi(response)
	if err != nil || idx < 1 || idx > len(scan.Findings) {
		return "", fmt.Errorf("invalid selection")
	}

	selected := scan.Findings[idx-1]
	return strings.ToLower(selected.ResourceKind) + "/" + selected.ResourceName, nil
}
