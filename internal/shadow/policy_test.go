package shadow

import "testing"

func TestDefaultPatchPolicyIsSafeFloor(t *testing.T) {
	p := DefaultPatchPolicy()
	if p.MaxMemoryBytes != 64*1024*1024*1024 {
		t.Fatalf("memory ceiling = %d", p.MaxMemoryBytes)
	}
	if p.MaxCPUMillicores != 32000 {
		t.Fatalf("cpu ceiling = %d", p.MaxCPUMillicores)
	}
	if !hostMatchesAny("ghcr.io", p.AllowedRegistries) {
		t.Fatal("ghcr.io should be in the default allowlist")
	}
}

func TestSetAndActivePatchPolicyRoundTrip(t *testing.T) {
	t.Cleanup(func() { SetPatchPolicy(DefaultPatchPolicy()) })
	SetPatchPolicy(PatchPolicy{AllowedRegistries: []string{"only.example.com"}, MaxMemoryBytes: 1, MaxCPUMillicores: 2})
	got := activePatchPolicy()
	if len(got.AllowedRegistries) != 1 || got.AllowedRegistries[0] != "only.example.com" {
		t.Fatalf("allowlist not set: %+v", got.AllowedRegistries)
	}
	if got.MaxMemoryBytes != 1 || got.MaxCPUMillicores != 2 {
		t.Fatalf("ceilings not set: %+v", got)
	}
}

func TestHostMatchesAny(t *testing.T) {
	patterns := DefaultPatchPolicy().AllowedRegistries
	allowed := []string{
		"ghcr.io", "registry-1.docker.io", "quay.io", "registry.k8s.io",
		"us-central1-docker.pkg.dev", "123456789.dkr.ecr.us-east-1.amazonaws.com", "myreg.azurecr.io",
	}
	for _, h := range allowed {
		if !hostMatchesAny(h, patterns) {
			t.Fatalf("expected %q allowed by default patterns", h)
		}
	}
	for _, h := range []string{"evil.io", "ghcr.io.evil.com", "example.com"} {
		if hostMatchesAny(h, patterns) {
			t.Fatalf("did not expect %q to be allowed", h)
		}
	}
}

func TestHostMatchesAnyGlobPatterns(t *testing.T) {
	// ? matches exactly one character (path.Match syntax).
	if !hostMatchesAny("gcr.io", []string{"gcr.i?"}) {
		t.Error("? pattern should match gcr.io")
	}
	// [...] character class.
	if !hostMatchesAny("ghcr.io", []string{"[gq]hcr.io"}) {
		t.Error("[gq] pattern should match ghcr.io")
	}
	// Non-matching host must return false.
	if hostMatchesAny("ecr.io", []string{"gcr.i?", "[gq]hcr.io"}) {
		t.Error("patterns should not match ecr.io")
	}
}
