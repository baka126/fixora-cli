package cli

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"

	"github.com/fixora/kubectl-fixora/internal/kube"
	"github.com/fixora/kubectl-fixora/internal/ops"
)

func executeRollback(ctx context.Context, k kube.Kubectl, rollback ops.Rollback) ([]byte, error) {
	switch rollback.Binary {
	case "kubectl":
		return k.Run(ctx, rollback.Args...)
	case "helm":
		cmd := exec.CommandContext(ctx, "helm", rollback.Args...)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			if stderr.Len() > 0 {
				return out, fmt.Errorf("%s", stderr.String())
			}
			return out, err
		}
		return out, nil
	default:
		return nil, fmt.Errorf("unsafe or unsupported rollback command %q", rollback.Command)
	}
}
