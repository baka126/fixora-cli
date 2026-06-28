package cli

import (
	"sort"
	"testing"

	"github.com/fixora/kubectl-fixora/internal/config"
	"github.com/fixora/kubectl-fixora/internal/shadow"
)

func TestPolicyFromConfigDefaults(t *testing.T) {
	p := policyFromConfig(config.Config{})
	def := shadowDefault()
	if p.MaxMemoryBytes != def.MaxMemoryBytes || p.MaxCPUMillicores != def.MaxCPUMillicores {
		t.Fatalf("empty config must yield default ceilings: %+v", p)
	}
	if len(p.AllowedRegistries) != len(def.AllowedRegistries) {
		t.Fatalf("empty config must yield default allowlist length: got %d want %d",
			len(p.AllowedRegistries), len(def.AllowedRegistries))
	}
	// Verify the allowlist contents match, not just the count.
	got := append([]string{}, p.AllowedRegistries...)
	want := append([]string{}, def.AllowedRegistries...)
	sort.Strings(got)
	sort.Strings(want)
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("allowlist mismatch at index %d: got %q want %q", i, got[i], want[i])
		}
	}
}

func TestPolicyFromConfigOverrides(t *testing.T) {
	p := policyFromConfig(config.Config{
		AllowedImageRegistries: []string{"only.example.com"},
		MaxPatchMemory:         "8Gi",
		MaxPatchCPU:            "4",
	})
	if len(p.AllowedRegistries) != 1 || p.AllowedRegistries[0] != "only.example.com" {
		t.Fatalf("allowlist override failed: %+v", p.AllowedRegistries)
	}
	if p.MaxMemoryBytes != 8*1024*1024*1024 {
		t.Fatalf("memory override failed: %d", p.MaxMemoryBytes)
	}
	if p.MaxCPUMillicores != 4000 {
		t.Fatalf("cpu override failed: %d", p.MaxCPUMillicores)
	}
}

func TestPolicyFromConfigBadQuantityKeepsDefault(t *testing.T) {
	def := shadowDefault()
	p := policyFromConfig(config.Config{MaxPatchMemory: "garbage"})
	if p.MaxMemoryBytes != def.MaxMemoryBytes {
		t.Fatalf("bad quantity must keep default, got %d", p.MaxMemoryBytes)
	}
}

func shadowDefault() shadow.PatchPolicy { return shadow.DefaultPatchPolicy() }
