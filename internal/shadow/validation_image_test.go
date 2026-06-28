package shadow

import "testing"

func TestImageRegistryAllowedHostPasses(t *testing.T) {
	reasons := validateImageRegistries(
		map[string]any{"containers": []any{map[string]any{"name": "app", "image": "ghcr.io/acme/api:v1"}}},
		map[string]any{"containers": []any{map[string]any{"name": "app", "image": "ghcr.io/acme/api:v2"}}},
		DefaultPatchPolicy(),
	)
	if len(reasons) != 0 {
		t.Fatalf("expected no reasons, got %v", reasons)
	}
}

func TestImageRegistryDisallowedHostRejected(t *testing.T) {
	reasons := validateImageRegistries(
		map[string]any{"containers": []any{map[string]any{"name": "app", "image": "ghcr.io/acme/api:v1"}}},
		map[string]any{"containers": []any{map[string]any{"name": "app", "image": "evil.io/acme/api:v2"}}},
		DefaultPatchPolicy(),
	)
	if len(reasons) == 0 {
		t.Fatal("expected a rejection reason for evil.io")
	}
}

func TestImageRegistryOriginalHostAutoAllowed(t *testing.T) {
	// myreg.internal is not in defaults, but it is the original image's host.
	reasons := validateImageRegistries(
		map[string]any{"containers": []any{map[string]any{"name": "app", "image": "myreg.internal/acme/api:v1"}}},
		map[string]any{"containers": []any{map[string]any{"name": "app", "image": "myreg.internal/acme/api:v2"}}},
		DefaultPatchPolicy(),
	)
	if len(reasons) != 0 {
		t.Fatalf("original host should be auto-allowed, got %v", reasons)
	}
}

func TestImageRegistryBareNameAllowed(t *testing.T) {
	reasons := validateImageRegistries(
		map[string]any{},
		map[string]any{"containers": []any{map[string]any{"name": "app", "image": "nginx:1.27"}}},
		DefaultPatchPolicy(),
	)
	if len(reasons) != 0 {
		t.Fatalf("bare docker-hub name should be allowed, got %v", reasons)
	}
}

func TestImageRegistryUnparseableRejected(t *testing.T) {
	reasons := validateImageRegistries(
		map[string]any{},
		map[string]any{"containers": []any{map[string]any{"name": "app", "image": "!! not a ref"}}},
		DefaultPatchPolicy(),
	)
	if len(reasons) == 0 {
		t.Fatal("expected a rejection for an unparseable image reference")
	}
}

func TestImageRegistryPlaceholderSkipped(t *testing.T) {
	reasons := validateImageRegistries(
		map[string]any{},
		map[string]any{"containers": []any{map[string]any{"name": "app", "image": "TODO_PINNED_MULTI_ARCH_IMAGE"}}},
		DefaultPatchPolicy(),
	)
	if len(reasons) != 0 {
		t.Fatalf("placeholder image must be skipped, got %v", reasons)
	}
}

func TestImageRegistryUppercaseHostAllowed(t *testing.T) {
	// GHCR.IO is the same registry as ghcr.io — must not be rejected.
	reasons := validateImageRegistries(
		map[string]any{"containers": []any{map[string]any{"name": "app", "image": "ghcr.io/acme/api:v1"}}},
		map[string]any{"containers": []any{map[string]any{"name": "app", "image": "GHCR.IO/acme/api:v2"}}},
		DefaultPatchPolicy(),
	)
	if len(reasons) != 0 {
		t.Fatalf("uppercase registry host should be allowed case-insensitively, got %v", reasons)
	}
}

func TestImageRegistryDisallowedInitContainerRejected(t *testing.T) {
	// Disallowed registry in initContainers must be caught.
	reasons := validateImageRegistries(
		map[string]any{"initContainers": []any{map[string]any{"name": "init", "image": "ghcr.io/acme/init:v1"}}},
		map[string]any{"initContainers": []any{map[string]any{"name": "init", "image": "evil.io/acme/init:v2"}}},
		DefaultPatchPolicy(),
	)
	if len(reasons) == 0 {
		t.Fatal("expected rejection for disallowed registry in initContainers")
	}
}
