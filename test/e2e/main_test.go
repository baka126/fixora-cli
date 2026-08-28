//go:build e2e

package e2e

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const defaultCluster = "fixora-e2e"

func TestMain(m *testing.M) {
	code, err := setupAndRun(m)
	if err != nil {
		fmt.Fprintf(os.Stderr, "e2e setup failed: %v\n", err)
		os.Exit(1)
	}
	os.Exit(code)
}

func setupAndRun(m *testing.M) (int, error) {
	cluster := envOr("FIXORA_E2E_KIND_CLUSTER", defaultCluster)
	kubeContext = "kind-" + cluster

	tmp, err := os.MkdirTemp("", "fixora-e2e-*")
	if err != nil {
		return 0, err
	}
	defer os.RemoveAll(tmp)

	// 1. Build the binary under test, unless one was supplied.
	binPath = os.Getenv("FIXORA_E2E_BIN")
	if binPath == "" {
		binPath = filepath.Join(tmp, "kubectl-fixora")
		build := exec.Command("go", "build", "-o", binPath, "../../cmd/kubectl-fixora")
		build.Stdout, build.Stderr = os.Stderr, os.Stderr
		if err := build.Run(); err != nil {
			return 0, fmt.Errorf("build binary: %w", err)
		}
	}

	// 2. Create the cluster if absent.
	created := false
	if !clusterExists(cluster) {
		create := exec.Command("kind", "create", "cluster", "--name", cluster)
		create.Stdout, create.Stderr = os.Stderr, os.Stderr
		if err := create.Run(); err != nil {
			return 0, fmt.Errorf("kind create cluster: %w", err)
		}
		created = true
	}
	if created && os.Getenv("FIXORA_E2E_KEEP") != "1" {
		defer func() {
			del := exec.Command("kind", "delete", "cluster", "--name", cluster)
			del.Stdout, del.Stderr = os.Stderr, os.Stderr
			_ = del.Run()
		}()
	}

	// 3. Write a dedicated kubeconfig so tests never touch the user's default.
	kubeconfig = filepath.Join(tmp, "kubeconfig")
	out, err := exec.Command("kind", "get", "kubeconfig", "--name", cluster).Output()
	if err != nil {
		return 0, fmt.Errorf("kind get kubeconfig: %w", err)
	}
	if err := os.WriteFile(kubeconfig, out, 0o600); err != nil {
		return 0, err
	}

	return m.Run(), nil
}

func clusterExists(name string) bool {
	out, err := exec.Command("kind", "get", "clusters").Output()
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.TrimSpace(line) == name {
			return true
		}
	}
	return false
}

func envOr(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}
