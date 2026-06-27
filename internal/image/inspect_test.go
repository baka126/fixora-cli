package image

import "testing"

func TestParsePlatformsFromIndex(t *testing.T) {
	platforms, err := parsePlatforms([]byte(`{
  "manifests": [
    {"platform":{"os":"linux","architecture":"amd64"}},
    {"platform":{"os":"linux","architecture":"arm64","variant":"v8"}}
  ]
}`))
	if err != nil {
		t.Fatal(err)
	}
	result := Result{Platforms: platforms}
	if !result.Supports("linux", "arm64") || result.Supports("linux", "s390x") {
		t.Fatalf("unexpected platform support: %#v", platforms)
	}
}

func TestParsePlatformsRejectsPlatformlessManifest(t *testing.T) {
	if _, err := parsePlatforms([]byte(`{"schemaVersion":2}`)); err == nil {
		t.Fatal("expected platformless manifest rejection")
	}
}

func TestParseReference(t *testing.T) {
	tests := []struct {
		value      string
		registry   string
		repository string
		reference  string
	}{
		{value: "polinux/stress-ng", registry: "registry-1.docker.io", repository: "polinux/stress-ng", reference: "latest"},
		{value: "nginx:1.27", registry: "registry-1.docker.io", repository: "library/nginx", reference: "1.27"},
		{value: "ghcr.io/acme/api@sha256:abc", registry: "ghcr.io", repository: "acme/api", reference: "sha256:abc"},
		{value: "ghcr.io/acme/api:v1@sha256:abc", registry: "ghcr.io", repository: "acme/api", reference: "sha256:abc"},
	}
	for _, tt := range tests {
		t.Run(tt.value, func(t *testing.T) {
			got, err := parseReference(tt.value)
			if err != nil {
				t.Fatal(err)
			}
			if got.Registry != tt.registry || got.Repository != tt.repository || got.Reference != tt.reference {
				t.Fatalf("unexpected reference: %#v", got)
			}
		})
	}
}

func TestRegistry(t *testing.T) {
	registry, err := Registry("polinux/stress-ng")
	if err != nil || registry != "registry-1.docker.io" {
		t.Fatalf("unexpected registry %q err=%v", registry, err)
	}
}

func TestRepository(t *testing.T) {
	repository, err := Repository("nginx:1.27")
	if err != nil || repository != "registry-1.docker.io/library/nginx" {
		t.Fatalf("unexpected repository %q err=%v", repository, err)
	}
}

func TestReferenceWithTag(t *testing.T) {
	parsed, err := parseReference("polinux/stress-ng")
	if err != nil {
		t.Fatal(err)
	}
	if got := parsed.withTag("arm64"); got != "polinux/stress-ng:arm64" {
		t.Fatalf("unexpected Docker Hub candidate %q", got)
	}
	parsed, err = parseReference("ghcr.io/acme/api:v1")
	if err != nil {
		t.Fatal(err)
	}
	if got := parsed.withTag("v2"); got != "ghcr.io/acme/api:v2" {
		t.Fatalf("unexpected registry candidate %q", got)
	}
}

func TestPinnedReference(t *testing.T) {
	result := Result{Reference: "repo/api:v1", Digest: "sha256:abc"}
	if got := result.PinnedReference(); got != "repo/api@sha256:abc" {
		t.Fatalf("unexpected pinned reference %q", got)
	}
}
