package termui

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/fixora/kubectl-fixora/internal/kube"
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
