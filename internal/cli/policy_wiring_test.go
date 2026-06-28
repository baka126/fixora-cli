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

func TestPolicyFromConfigFiltersMalformedRegistries(t *testing.T) {
	p := policyFromConfig(config.Config{
		AllowedImageRegistries: []string{"", "bad[", "REGISTRY.EXAMPLE.COM", "has/path"},
	})
	if len(p.AllowedRegistries) != 1 || p.AllowedRegistries[0] != "registry.example.com" {
		t.Fatalf("expected only the usable registry pattern, got %+v", p.AllowedRegistries)
	}
}

func TestPolicyFromConfigMalformedRegistriesKeepDefault(t *testing.T) {
	def := shadowDefault()
	p := policyFromConfig(config.Config{
		AllowedImageRegistries: []string{"", "bad[", "has/path"},
	})
	if len(p.AllowedRegistries) != len(def.AllowedRegistries) {
		t.Fatalf("all malformed registry patterns must keep defaults, got %+v", p.AllowedRegistries)
	}
}

func TestPolicyFromConfigBadQuantityKeepsDefault(t *testing.T) {
	def := shadowDefault()
	p := policyFromConfig(config.Config{MaxPatchMemory: "garbage"})
	if p.MaxMemoryBytes != def.MaxMemoryBytes {
		t.Fatalf("bad quantity must keep default, got %d", p.MaxMemoryBytes)
	}
}

func TestPolicyFromConfigNonPositiveQuantityKeepsDefault(t *testing.T) {
	def := shadowDefault()
	p := policyFromConfig(config.Config{MaxPatchMemory: "-1Gi", MaxPatchCPU: "0"})
	if p.MaxMemoryBytes != def.MaxMemoryBytes {
		t.Fatalf("negative memory ceiling must keep default, got %d", p.MaxMemoryBytes)
	}
	if p.MaxCPUMillicores != def.MaxCPUMillicores {
		t.Fatalf("zero cpu ceiling must keep default, got %d", p.MaxCPUMillicores)
	}
}

func shadowDefault() shadow.PatchPolicy { return shadow.DefaultPatchPolicy() }
