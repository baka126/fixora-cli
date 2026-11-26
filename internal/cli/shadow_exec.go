package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/fixora/kubectl-fixora/internal/kube"
	"github.com/fixora/kubectl-fixora/internal/shadow"
	"github.com/fixora/kubectl-fixora/internal/termui"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

type shadowShellRunner func(context.Context, kube.Kubectl, string, string, string) error
type shadowExecPermission func(context.Context, kube.Kubectl, string) (bool, error)

func inspectShadowAfterVerdict(ctx context.Context, stdout, stderr io.Writer, opts options, kubectl kube.Kubectl, client *kube.TypedClient, result shadow.Result, verified bool, canExec shadowExecPermission, runner shadowShellRunner) error {
	if result.IsolationNamespace == "" {
		return fmt.Errorf("no isolated namespace is available")
	}
	if !opts.interactive || opts.output != "text" {
		return fmt.Errorf("shadow shell inspection requires interactive text output on a TTY")
	}
	if !verified || !result.Verified || result.Status != shadow.VerificationVerified {
		if !opts.keepShadow {
			return deleteRetainedShadowNamespace(stdout, client, result)
		}
		fmt.Fprintf(stdout, "Kept isolated namespace: %s (default-deny ingress and egress)\n", result.IsolationNamespace)
		return nil
	}

	if termui.Confirm("Open a shell in the verified shadow Pod? The shell can modify only this isolated candidate.", inputFor(opts), stdout) {
		container, err := validateShadowShellTarget(ctx, client, result, opts.container)
		if err != nil {
			if !opts.keepShadow {
				if cleanupErr := deleteRetainedShadowNamespace(stdout, client, result); cleanupErr != nil {
					return fmt.Errorf("%v; cleanup failed: %w", err, cleanupErr)
				}
			}
			return err
		}
		allowed, err := canExec(ctx, kubectl, result.IsolationNamespace)
		if err != nil || !allowed {
			if !opts.keepShadow {
				if cleanupErr := deleteRetainedShadowNamespace(stdout, client, result); cleanupErr != nil {
					return fmt.Errorf("pods/exec permission unavailable; cleanup failed: %w", cleanupErr)
				}
			}
			if err != nil {
				return fmt.Errorf("check pods/exec permission: %w", err)
			}
			return fmt.Errorf("pods/exec permission is not allowed in isolated namespace %s", result.IsolationNamespace)
		}
		fmt.Fprintf(stdout, "Opening shell in pod/%s container/%s in %s. Exit the shell to continue.\n", result.CloneName, container, result.IsolationNamespace)
		if err := runner(ctx, kubectl, result.IsolationNamespace, result.CloneName, container); err != nil {
			if !opts.keepShadow {
				if cleanupErr := deleteRetainedShadowNamespace(stdout, client, result); cleanupErr != nil {
					return fmt.Errorf("kubectl exec failed: %v; cleanup failed: %w", err, cleanupErr)
				}
			}
			return fmt.Errorf("kubectl exec failed: %w", err)
		}
	} else {
		fmt.Fprintln(stdout, "Shell skipped.")
	}
	if opts.keepShadow {
		fmt.Fprintf(stdout, "Kept isolated namespace: %s (default-deny ingress and egress)\n", result.IsolationNamespace)
		return nil
	}
	return deleteRetainedShadowNamespace(stdout, client, result)
}

func validateShadowShellTarget(ctx context.Context, client *kube.TypedClient, result shadow.Result, requestedContainer string) (string, error) {
	if client == nil || client.Clientset == nil {
		return "", fmt.Errorf("typed Kubernetes client is unavailable")
	}
	if result.IsolationNamespace == "" || result.IsolationUID == "" || result.CloneName == "" || result.CloneUID == "" {
		return "", fmt.Errorf("shadow result is missing exact namespace or Pod identity; refusing exec")
	}
	candidates, err := shadow.ListCleanupCandidates(ctx, client)
	if err != nil {
		return "", err
	}
	owned := false
	var session string
	for _, candidate := range candidates {
		if candidate.Namespace == result.IsolationNamespace && candidate.UID == result.IsolationUID {
			owned = true
			session = candidate.Session
			break
		}
	}
	if !owned {
		return "", fmt.Errorf("isolated namespace identity or Fixora ownership changed; refusing exec")
	}
	pod, err := client.GetTypedPod(ctx, result.IsolationNamespace, result.CloneName)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return "", fmt.Errorf("verified shadow Pod no longer exists; refusing exec")
		}
		return "", fmt.Errorf("re-read verified shadow Pod: %w", err)
	}
	if string(pod.UID) != result.CloneUID || pod.Labels["fixora.io/sandbox"] != "true" || pod.Labels["fixora.io/session"] != session || pod.Labels["fixora.io/original-pod"] == "" {
		return "", fmt.Errorf("verified shadow Pod identity or ownership changed; refusing exec")
	}
	if len(pod.Spec.Containers) == 0 {
		return "", fmt.Errorf("verified shadow Pod has no regular containers; refusing exec")
	}
	if requestedContainer != "" {
		for _, container := range pod.Spec.Containers {
			if container.Name == requestedContainer {
				return requestedContainer, nil
			}
		}
		return "", fmt.Errorf("container %q is not present in the verified shadow Pod", requestedContainer)
	}
	if len(pod.Spec.Containers) != 1 {
		return "", fmt.Errorf("verified shadow Pod has multiple containers; specify --container")
	}
	return pod.Spec.Containers[0].Name, nil
}

func kubectlAllowsPodExec(ctx context.Context, k kube.Kubectl, namespace string) (bool, error) {
	out, err := k.Run(ctx, "auth", "can-i", "create", "pods/exec", "-n", namespace)
	if err != nil {
		return false, err
	}
	return strings.EqualFold(strings.TrimSpace(string(out)), "yes"), nil
}

func runKubectlShadowShell(ctx context.Context, k kube.Kubectl, namespace, pod, container string) error {
	args := []string{}
	if k.Context != "" {
		args = append(args, "--context", k.Context)
	}
	args = append(args, "exec", "-it", "-n", namespace, pod, "-c", container, "--", "/bin/sh")
	cmd := exec.CommandContext(ctx, "kubectl", args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func deleteRetainedShadowNamespace(stdout io.Writer, client *kube.TypedClient, result shadow.Result) error {
	if result.IsolationNamespace == "" || result.IsolationUID == "" {
		return fmt.Errorf("cannot clean retained shadow namespace without exact namespace UID")
	}
	cleanupCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	candidates, err := shadow.ListCleanupCandidates(cleanupCtx, client)
	if err != nil {
		return err
	}
	for _, candidate := range candidates {
		if candidate.Namespace == result.IsolationNamespace && candidate.UID == result.IsolationUID {
			if err := shadow.DeleteCleanupCandidate(cleanupCtx, client, candidate); err != nil {
				return err
			}
			fmt.Fprintf(stdout, "Cleaned up namespace/%s.\n", result.IsolationNamespace)
			return nil
		}
	}
	// If the exact namespace is already gone, cleanup is complete. A recreated or
	// relabeled namespace is never deleted through this path.
	current, err := client.GetNamespace(cleanupCtx, result.IsolationNamespace)
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("check retained namespace before cleanup: %w", err)
	}
	if string(current.UID) != result.IsolationUID {
		return fmt.Errorf("retained namespace was replaced; refusing cleanup")
	}
	return fmt.Errorf("retained namespace no longer matches Fixora cleanup ownership; refusing deletion")
}
