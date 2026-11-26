package infer

import (
	"context"
	"github.com/fixora/kubectl-fixora/internal/analyzer"
	"github.com/fixora/kubectl-fixora/internal/fix"
	"path"
	"regexp"
	"strings"
)

func init() { register(filesystemInferrer{}) }

type filesystemInferrer struct{}

func (filesystemInferrer) Name() string { return "filesystem" }
func (filesystemInferrer) Handles(p fix.Plan) bool {
	return p.Strategy == "security" || p.Strategy == "runtime"
}

var literalCacheTouch = regexp.MustCompile(`(?:^|;\s*|if\s+!\s+)touch\s+(/[A-Za-z0-9_./-]+)(?:\s|;|$)`)

func (filesystemInferrer) Infer(ctx context.Context, r kubeReader, f analyzer.Finding, _ fix.Plan) (Result, bool, error) {
	if r == nil || f.PodName == "" || (f.Status != "PermissionDenied" && f.Status != "CrashLoopBackOff") {
		return Result{}, false, nil
	}
	denied := false
	for _, log := range f.Logs {
		s := strings.ToLower(log.Text)
		if strings.Contains(s, "permission denied") && (strings.Contains(s, "cache") || strings.Contains(s, "/tmp/")) {
			denied = true
		}
	}
	if !denied {
		return Result{}, false, nil
	}
	pod, err := r.GetResource(ctx, f.Namespace, "pod/"+f.PodName)
	if err != nil {
		return Result{}, false, err
	}
	meta, _ := pod["metadata"].(map[string]any)
	if meta["name"] != f.PodName || meta["namespace"] != f.Namespace {
		return Result{}, false, nil
	}
	spec, _ := pod["spec"].(map[string]any)
	containers, _ := spec["containers"].([]any)
	if len(containers) != 1 {
		return Result{}, false, nil
	}
	if volumes, _ := spec["volumes"].([]any); hasNonServiceAccountVolume(volumes) {
		return Result{}, false, nil
	}
	if init, _ := spec["initContainers"].([]any); len(init) > 0 {
		return Result{}, false, nil
	}
	c, _ := containers[0].(map[string]any)
	name, _ := c["name"].(string)
	if !cacheWriteIsCurrentFailure(pod, name) {
		return Result{}, false, nil
	}
	sc, _ := c["securityContext"].(map[string]any)
	if sc["readOnlyRootFilesystem"] != true || sc["runAsNonRoot"] != true {
		return Result{}, false, nil
	}
	uid := positiveID(sc["runAsUser"])
	if uid <= 0 {
		return Result{}, false, nil
	}
	mounts, _ := c["volumeMounts"].([]any)
	for _, raw := range mounts {
		m, _ := raw.(map[string]any)
		mp, _ := m["mountPath"].(string)
		if mp != "/var/run/secrets/kubernetes.io/serviceaccount" {
			return Result{}, false, nil
		}
	}
	cmd, _ := c["command"].([]any)
	if len(cmd) != 3 || (cmd[0] != "sh" && cmd[0] != "/bin/sh") || cmd[1] != "-c" {
		return Result{}, false, nil
	}
	script, _ := cmd[2].(string)
	matches := literalCacheTouch.FindAllStringSubmatch(script, -1)
	if len(matches) != 1 || strings.Count(script, "touch ") != 1 {
		return Result{}, false, nil
	}
	file := matches[0][1]
	if path.Clean(file) != file {
		return Result{}, false, nil
	}
	dir := path.Dir(file)
	if !fix.SafeCacheDirectory(dir) {
		return Result{}, false, nil
	}
	group := uid
	podSC, _ := spec["securityContext"].(map[string]any)
	if raw, exists := podSC["fsGroup"]; exists {
		group = positiveID(raw)
		if group <= 0 {
			return Result{}, false, nil
		}
	}
	return Result{EphemeralCache: &fix.EphemeralCache{Container: name, Directory: dir, Group: group}, Guardrail: "inferred-cache-from-literal-write", Warning: "A literal cache write fails under a read-only root filesystem; verify the inferred emptyDir mount in shadow before delivery."}, true, nil
}

func cacheWriteIsCurrentFailure(pod map[string]any, name string) bool {
	status, _ := pod["status"].(map[string]any)
	items, _ := status["containerStatuses"].([]any)
	for _, raw := range items {
		cs, _ := raw.(map[string]any)
		if cs["name"] != name || cs["ready"] != false {
			continue
		}
		state, _ := cs["state"].(map[string]any)
		waiting, _ := state["waiting"].(map[string]any)
		if waiting["reason"] != "CrashLoopBackOff" {
			continue
		}
		last, _ := cs["lastState"].(map[string]any)
		terminated, _ := last["terminated"].(map[string]any)
		if terminated["reason"] == "Error" && positiveID(terminated["exitCode"]) == 1 {
			return true
		}
	}
	return false
}

func positiveID(value any) int64 {
	switch n := value.(type) {
	case float64:
		if n > 0 && n <= 4294967294 && n == float64(int64(n)) {
			return int64(n)
		}
	case int:
		if n > 0 {
			return int64(n)
		}
	case int64:
		if n > 0 {
			return n
		}
	}
	return 0
}
func hasNonServiceAccountVolume(volumes []any) bool {
	for _, raw := range volumes {
		v, _ := raw.(map[string]any)
		name, _ := v["name"].(string)
		if !strings.HasPrefix(name, "kube-api-access-") || v["projected"] == nil {
			return true
		}
	}
	return false
}
