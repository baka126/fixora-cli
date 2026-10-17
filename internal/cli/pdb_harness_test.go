package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestPDBHarnessReachesOnlyDedicatedClusterCreation(t *testing.T) {
	root := t.TempDir()
	log := filepath.Join(root, "calls")
	for _, tool := range []string{"docker", "go", "kubectl"} {
		if e := os.WriteFile(filepath.Join(root, tool), []byte("#!/bin/sh\nexit 0\n"), 0700); e != nil {
			t.Fatal(e)
		}
	}
	kind := `#!/bin/sh
printf '%s\n' "$*" >> "$PDB_HARNESS_LOG"
case "$*" in
  'get clusters') exit 0 ;;
  'create cluster --name fixora-pdb-e2e-'*) exit 73 ;;
  *) exit 99 ;;
esac
`
	os.WriteFile(filepath.Join(root, "kind"), []byte(kind), 0700)
	cmd := exec.Command("bash", "../../hack/e2e-pdb.sh")
	cmd.Env = append(os.Environ(), "PATH="+root+":"+os.Getenv("PATH"), "PDB_HARNESS_LOG="+log)
	b, e := cmd.CombinedOutput()
	exit, ok := e.(*exec.ExitError)
	if !ok || exit.ExitCode() != 73 {
		t.Fatalf("did not reach bounded local creation: %v %s", e, b)
	}
	calls, _ := os.ReadFile(log)
	lines := strings.Split(strings.TrimSpace(string(calls)), "\n")
	if len(lines) != 2 || lines[0] != "get clusters" || !strings.HasPrefix(lines[1], "create cluster --name fixora-pdb-e2e-") {
		t.Fatal("unexpected cluster operation", string(calls))
	}
}
