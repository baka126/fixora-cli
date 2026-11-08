package fix

import (
	"fmt"
	"github.com/fixora/kubectl-fixora/internal/kube"
	"gopkg.in/yaml.v3"
	"path"
	"strings"
)

// EphemeralCache describes a proven transient write location. It never changes
// the application's command, identity, or read-only root filesystem.
type EphemeralCache struct {
	Container, Directory string
	Group                int64
}

func WithEphemeralCachePatch(p Plan, c EphemeralCache) Plan {
	if (p.Strategy != "security" && p.Strategy != "runtime") || c.Container == "" || c.Group <= 0 || c.Group > 4294967294 || !SafeCacheDirectory(c.Directory) || p.Recovered {
		return p
	}
	kind, name, ok := strings.Cut(p.Resource, "/")
	if !ok || name == "" || p.Namespace == "" {
		return p
	}
	if kube.WorkloadVersion(kind) == "" {
		return p
	}
	for _, reason := range p.BlockedReasons {
		if strings.Contains(reason, "GitOps") {
			return p
		}
	}
	spec := map[string]any{"securityContext": map[string]any{"fsGroup": c.Group}, "volumes": []any{map[string]any{"name": "fixora-cache", "emptyDir": map[string]any{}}}, "containers": []any{map[string]any{"name": c.Container, "volumeMounts": []any{map[string]any{"name": "fixora-cache", "mountPath": c.Directory}}}}}
	obj, err := kube.WorkloadPatch(kind, name, p.Namespace, spec)
	if err != nil {
		return p
	}
	data, err := yaml.Marshal(obj)
	if err != nil {
		return p
	}
	p.PatchTemplate = string(data)
	p.Strategy = "filesystem"
	p.CanApply = true
	p.Safe = true
	p.Risk = "low"
	p.Confidence = 95
	p.BlockedReasons = nil
	p.Patches = []Patch{{Type: "strategic-merge", Target: p.Resource, Description: "Mount an ephemeral writable cache while preserving container security controls.", Preview: p.PatchTemplate}}
	p.Warnings = append(p.Warnings, fmt.Sprintf("%s becomes ephemeral storage; contents are lost when the pod is replaced. Confirm this path holds only transient cache data.", c.Directory))
	p.Guardrails = append(p.Guardrails, "proven-ephemeral-cache-write", "security-controls-preserved", "shadow-required")
	p.refreshApplyEligibility(false)
	return p
}

// SafeCacheDirectory restricts inferred mounts to transient scratch/cache roots.
func SafeCacheDirectory(dir string) bool {
	return path.IsAbs(dir) && path.Clean(dir) == dir && !strings.ContainsAny(dir, "\n\r\x00") && (dir == "/var/cache" || strings.HasPrefix(dir, "/var/cache/") || dir == "/tmp" || strings.HasPrefix(dir, "/tmp/"))
}
