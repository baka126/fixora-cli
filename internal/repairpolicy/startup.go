package repairpolicy

import (
	"context"
	"errors"
	"reflect"
	"strings"

	"github.com/fixora/kubectl-fixora/internal/kube"
	"gopkg.in/yaml.v3"
)

func Startup(s Snapshot, a *Authority, selected string) (string, *Proof, error) {
	if _, err := snapshotBinding(s); err != nil {
		return "", nil, err
	}
	if a == nil || a.digest == "" || a.digest != digest(a.object) {
		return "", nil, errors.New("explicit startup authority is required")
	}
	if err := identityMatches(a.object, s.Target); err != nil {
		return "", nil, err
	}
	if s.Pod == nil {
		return "", nil, errors.New("startup restoration requires a current failing Pod")
	}
	for _, c := range s.Pod.Status.InitStatuses {
		if failed(c) {
			return "", nil, errors.New("init container failure requires separate diagnosis")
		}
	}
	failures := []string{}
	seen := map[string]bool{}
	for _, c := range s.Pod.Status.ContainerStatuses {
		if seen[c.Name] {
			return "", nil, errors.New("ambiguous container status")
		}
		seen[c.Name] = true
		if failed(c) {
			failures = append(failures, c.Name)
		}
	}
	if len(failures) != 1 || (selected != "" && selected != failures[0]) {
		return "", nil, errors.New("startup restoration requires one uniquely failing regular container")
	}
	selected = failures[0]
	liveSpec, _ := kube.WorkloadPodSpec(s.Object)
	baseSpec, _ := kube.WorkloadPodSpec(a.object)
	live, err := containerMap(liveSpec)
	if err != nil {
		return "", nil, err
	}
	base, err := containerMap(baseSpec)
	if err != nil {
		return "", nil, err
	}
	if len(live) != len(base) {
		return "", nil, errors.New("startup baseline container identities must match the live workload")
	}
	for name, c := range live {
		b := base[name]
		if b == nil || c["image"] == nil || c["image"] == "" || b["image"] != c["image"] {
			return "", nil, errors.New("startup baseline container image must match the live workload")
		}
	}
	c := live[selected]
	b := base[selected]
	if c == nil || b == nil {
		return "", nil, errors.New("failing container does not match baseline")
	}
	command, ok := stringArray(b["command"])
	if !ok || len(command) == 0 || strings.TrimSpace(command[0]) == "" {
		return "", nil, errors.New("startup baseline requires a nonempty command array")
	}
	args := []string{}
	if raw, exists := b["args"]; exists {
		args, ok = stringArray(raw)
		if !ok {
			return "", nil, errors.New("startup args must be a string array")
		}
	}
	currentCommand, currentArgs := []string{}, []string{}
	for key, dst := range map[string]*[]string{"command": &currentCommand, "args": &currentArgs} {
		if raw, exists := c[key]; exists {
			v, valid := stringArray(raw)
			if !valid {
				return "", nil, errors.New("live startup fields are malformed")
			}
			*dst = v
		}
	}
	if reflect.DeepEqual(command, currentCommand) && reflect.DeepEqual(args, currentArgs) {
		return "", nil, errors.New("startup baseline already matches; no restoration is needed")
	}
	found := false
	for _, pc := range s.Pod.Spec.Containers {
		if pc.Name == selected {
			if found || pc.Image != c["image"] || !equalStrings(pc.Command, currentCommand) || !equalStrings(pc.Args, currentArgs) {
				return "", nil, errors.New("failing Pod belongs to a different startup revision")
			}
			found = true
		}
	}
	if !found {
		return "", nil, errors.New("regular failing container was not found")
	}
	obj, err := kube.WorkloadPatch(s.Target.Kind, s.Target.Name, s.Target.Namespace, map[string]any{"containers": []any{map[string]any{"name": selected, "command": command, "args": args}}})
	if err != nil {
		return "", nil, err
	}
	data, err := yaml.Marshal(obj)
	if err != nil {
		return "", nil, err
	}
	p, err := bind(s, string(data), "restore-command", a, []string{"containers/" + selected + "/command", "containers/" + selected + "/args"})
	if err != nil {
		return "", nil, err
	}
	p.summary.Caveats = []string{"The operator supplied the startup baseline; mutable image tags do not prove historical image identity."}
	return string(data), p, nil
}
func failed(c kube.ContainerStatus) bool {
	if c.Ready {
		return false
	}
	if w, ok := c.State["waiting"]; ok && w.Reason == "CrashLoopBackOff" {
		return true
	}
	if term, ok := c.State["terminated"]; ok && term.ExitCode != 0 {
		return true
	}
	return false
}
func containerMap(spec map[string]any) (map[string]map[string]any, error) {
	raw, ok := spec["containers"].([]any)
	if !ok || len(raw) == 0 {
		return nil, errors.New("regular containers are missing")
	}
	out := map[string]map[string]any{}
	for _, v := range raw {
		c, ok := v.(map[string]any)
		if !ok {
			return nil, errors.New("container must be an object")
		}
		name, ok := c["name"].(string)
		if !ok || name == "" || out[name] != nil {
			return nil, errors.New("container identity is missing or ambiguous")
		}
		out[name] = c
	}
	return out, nil
}
func stringArray(raw any) ([]string, bool) {
	values, ok := copyValue(raw).([]any)
	if !ok {
		return nil, false
	}
	out := make([]string, 0, len(values))
	for _, v := range values {
		s, ok := v.(string)
		if !ok || strings.ContainsRune(s, 0) {
			return nil, false
		}
		out = append(out, s)
	}
	return out, true
}
func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
func VerifyStartup(ctx context.Context, r kube.Reader, p *Proof, pod *kube.Pod, patch string) (*Receipt, error) {
	if p == nil || p.family != "restore-command" || p.authority == "" {
		return nil, errors.New("trusted startup proof is required")
	}
	return witness(ctx, r, p, pod, patch)
}
