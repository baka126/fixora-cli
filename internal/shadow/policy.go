package shadow

import (
	"path"
	"strings"
)

// PatchPolicy bounds the values an apply-eligible patch may set. It is a
// set-once, read-mostly process-wide configured singleton: DefaultPatchPolicy
// is the safe floor enforced unless SetPatchPolicy overrides it at startup.
type PatchPolicy struct {
	AllowedRegistries []string // image registry host globs (path.Match syntax)
	MaxMemoryBytes    int64    // 0 = unlimited
	MaxCPUMillicores  int64    // 0 = unlimited
}

// DefaultPatchPolicy is the built-in safe floor.
func DefaultPatchPolicy() PatchPolicy {
	return PatchPolicy{
		AllowedRegistries: []string{
			"registry-1.docker.io", "docker.io", "index.docker.io",
			"registry.k8s.io", "gcr.io", "ghcr.io", "quay.io",
			"public.ecr.aws", "mcr.microsoft.com", "registry.gitlab.com",
			"*.pkg.dev", "*.dkr.ecr.*.amazonaws.com", "*.azurecr.io",
		},
		MaxMemoryBytes:   64 * 1024 * 1024 * 1024,
		MaxCPUMillicores: 32000,
	}
}

var patchPolicy = DefaultPatchPolicy()

// SetPatchPolicy overrides the process-wide policy. Call once at startup.
func SetPatchPolicy(p PatchPolicy) { patchPolicy = p }

// activePatchPolicy returns the policy the validators enforce.
func activePatchPolicy() PatchPolicy { return patchPolicy }

// hostMatchesAny reports whether host equals or glob-matches any pattern.
func hostMatchesAny(host string, patterns []string) bool {
	for _, p := range patterns {
		if p == host {
			return true
		}
		if strings.Contains(p, "*") {
			if ok, _ := path.Match(p, host); ok {
				return true
			}
		}
	}
	return false
}
