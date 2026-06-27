package termui

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/fixora/kubectl-fixora/internal/kube"
	"github.com/fixora/kubectl-fixora/internal/repo"
)

func ConfirmApply(ctx context.Context, k kube.Reader, patch string, in io.Reader, out io.Writer) bool {
	if in == nil {
		in = os.Stdin
	}
	if out == nil {
		out = os.Stdout
	}
	file, err := os.CreateTemp("", "fixora-diff-*.yaml")
	if err == nil {
		file.WriteString(patch)
		file.Close()
		diffOut, _ := k.Run(ctx, "diff", "-f", file.Name())
		os.Remove(file.Name())
		if len(diffOut) > 0 {
			fmt.Fprintln(out, string(diffOut))
		} else {
			fmt.Fprintln(out, patch)
		}
	} else {
		fmt.Fprintln(out, patch)
	}
	fmt.Fprint(out, "\nWould you like to apply this fix? [y/N]: ")
	reader := bufio.NewReader(in)
	response, err := reader.ReadString('\n')
	if err != nil {
		return false
	}
	response = strings.TrimSpace(response)
	response = strings.ToLower(response)
	if response == "y" {
		return true
	}
	return false
}

func ConfirmShadowDeploy(diff string, in io.Reader, out io.Writer) bool {
	if in == nil {
		in = os.Stdin
	}
	if out == nil {
		out = os.Stdout
	}
	if strings.TrimSpace(diff) != "" {
		fmt.Fprintln(out, diff)
	}
	fmt.Fprint(out, "\nDeploy shadow clone? [Y/n]: ")
	reader := bufio.NewReader(in)
	response, err := reader.ReadString('\n')
	if err != nil {
		return false
	}
	response = strings.ToLower(strings.TrimSpace(response))
	return response == "" || response == "y" || response == "yes"
}

func ConfirmEditPatch(path string, in io.Reader, out io.Writer) bool {
	if in == nil {
		in = os.Stdin
	}
	if out == nil {
		out = os.Stdout
	}
	fmt.Fprintf(out, "\nEdit generated patch %s before verification or delivery? [y/N]: ", path)
	reader := bufio.NewReader(in)
	response, err := reader.ReadString('\n')
	if err != nil {
		return false
	}
	response = strings.ToLower(strings.TrimSpace(response))
	return response == "y" || response == "yes"
}

func ConfirmRollback(command string, in io.Reader, out io.Writer) bool {
	if in == nil {
		in = os.Stdin
	}
	if out == nil {
		out = os.Stdout
	}
	fmt.Fprintf(out, "\nRollout did not become healthy. Run rollback now?\n  %s\n[y/N]: ", command)
	reader := bufio.NewReader(in)
	response, err := reader.ReadString('\n')
	if err != nil {
		return false
	}
	response = strings.ToLower(strings.TrimSpace(response))
	return response == "y" || response == "yes"
}

func ConfirmVerifiedDelivery(summary repo.ChangeSummary, in io.Reader, out io.Writer) bool {
	if in == nil {
		in = os.Stdin
	}
	if out == nil {
		out = os.Stdout
	}
	fmt.Fprintln(out, "\nVerified PR/MR delivery summary")
	fmt.Fprintf(out, "Branch: %s\n", firstNonEmpty(summary.Branch, "<current>"))
	if len(summary.Files) > 0 {
		fmt.Fprintf(out, "Files: %s\n", strings.Join(summary.Files, ", "))
	}
	if strings.TrimSpace(summary.Stat) != "" {
		fmt.Fprintf(out, "Diff summary:\n%s\n", summary.Stat)
	}
	if strings.TrimSpace(summary.Diff) != "" {
		fmt.Fprintf(out, "Diff preview:\n%s\n", summary.Diff)
	}
	if strings.TrimSpace(summary.Remote) != "" {
		fmt.Fprintf(out, "Remote: %s\n", summary.Remote)
	}
	fmt.Fprintf(out, "Action: %s\n", firstNonEmpty(summary.Provider, "branch push only"))
	fmt.Fprint(out, "\nPush verified PR/MR? [y/N]: ")
	reader := bufio.NewReader(in)
	response, err := reader.ReadString('\n')
	if err != nil {
		return false
	}
	response = strings.ToLower(strings.TrimSpace(response))
	return response == "y" || response == "yes"
}
