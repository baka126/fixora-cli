package infer

import (
	"context"
	"encoding/json"
	"math"
	"path"

	"fmt"
	"github.com/fixora/kubectl-fixora/internal/kube"
	"regexp"
	"strconv"
	"strings"

	"github.com/fixora/kubectl-fixora/internal/analyzer"
	"github.com/fixora/kubectl-fixora/internal/fix"
)

// limitHeadroom multiplies observed usage to pick a limit. A heuristic, and
// the surfaced warning says so — the plan already points at Prometheus p95.
const limitHeadroom = 1.5

// cpuFloorMillicores is the smallest CPU request worth proposing. A container
// sampled at 0m or 2m still needs a schedulable request, and — unlike memory —
// a low CPU sample is ordinary rather than a sign the sample is missing, so CPU
// is floored here rather than declined.
const cpuFloorMillicores = 10

func init() { register(resourcesInferrer{}) }

type resourcesInferrer struct{}

func (resourcesInferrer) Name() string { return "resources" }

func (resourcesInferrer) Handles(plan fix.Plan) bool {
	return strings.EqualFold(plan.Strategy, "resources")
}

func (resourcesInferrer) Infer(ctx context.Context, r kubeReader, f analyzer.Finding, _ fix.Plan) (Result, bool, error) {
	container := ContainerName(f)
	if container == "" {
		return Result{}, false, nil
	}
	// An OOM-clipped metric can understate a configured allocation. Read the
	// diagnosed pod to establish demand before falling back to observed usage.
	demand, demandOK := declaredOOMDemand(ctx, r, f, container)
	// OOMKilled: size from what the container actually used.
	top := EvidenceValue(f, "Metrics pod containers")
	observed, observedOK := parseTopMemory(top, container)
	if demandOK && (!observedOK || demand > observed) {
		cpu, _ := parseTopCPU(top, container)
		return Result{
			Options:   fix.ConcreteOptions{Container: container, MemoryRequest: fmt.Sprintf("%dMi", roundUpMi(demand)), MemoryLimit: fmt.Sprintf("%dMi", roundUpMi(demand+demand/2+demand%2)), CPURequest: fmt.Sprintf("%dm", max(cpu, cpuFloorMillicores))},
			Guardrail: "inferred-resources-from-declared-allocation",
			Warning:   fmt.Sprintf("The OOM-killed container explicitly allocates %dMi; memory request uses that demand and limit adds %.1fx headroom. CPU uses the observed sample or the %dm minimum. This is a workload-derived estimate; shadow verification and production p95 remain required.", demand, limitHeadroom, cpuFloorMillicores),
		}, true, nil
	}
	if observedOK {
		request := roundUpMi(observed)
		limit := roundUpMi(int64(float64(observed) * limitHeadroom))
		cpu, _ := parseTopCPU(top, container)
		cpu = max(cpu, cpuFloorMillicores)
		return Result{
			Options: fix.ConcreteOptions{
				Container:     container,
				MemoryRequest: fmt.Sprintf("%dMi", request),
				MemoryLimit:   fmt.Sprintf("%dMi", limit),
				CPURequest:    fmt.Sprintf("%dm", cpu),
			},
			Guardrail: "inferred-resources-from-metrics",
			Warning:   fmt.Sprintf("Request and limit inferred from observed usage (%dMi) with %.1fx headroom; confirm against p95 before production.", observed, limitHeadroom),
		}, true, nil
	}
	// Unschedulable: requests exceed every node's allocatable. The analyzer
	// already computed the largest node's allocatable memory and CPU when it
	// emitted this evidence, so size from that rather than recomputing here.
	if shortfall := EvidenceValue(f, "Requests exceed node allocatable"); shortfall != "" {
		mem, cpu, ok := parseAllocatable(shortfall)
		if !ok {
			return Result{}, false, nil
		}
		request := roundUpMi(mem / 2)
		cpuRequest := max(cpu/2, cpuFloorMillicores)
		return Result{
			Options: fix.ConcreteOptions{
				Container:     container,
				MemoryRequest: fmt.Sprintf("%dMi", request),
				// No observed usage justifies headroom; equal request and limit
				// yields Guaranteed QoS, the conservative default with no data.
				MemoryLimit: fmt.Sprintf("%dMi", request),
				CPURequest:  fmt.Sprintf("%dm", cpuRequest),
			},
			Guardrail: "inferred-resources-from-allocatable",
			Warning:   fmt.Sprintf("Requests lowered to half the largest node's allocatable (memory %dMi, CPU %dm) because no node could schedule the pod; confirm the workload runs within them.", mem, cpu),
		}, true, nil
	}
	return Result{}, false, nil
}

// allocatablePattern extracts the figures the analyzer computed when it emitted
// the "Requests exceed node allocatable" evidence. Producer and parser must
// stay in sync — see allocatableShortfall in internal/analyzer/analyzer.go.
var allocatablePattern = regexp.MustCompile(`largest node allocatable memory=(\d+)Mi cpu=(\d+)m`)

// parseAllocatable reads the largest node's allocatable memory (mebibytes) and
// CPU (millicores) out of that evidence value.
func parseAllocatable(evidence string) (mem int64, cpu int64, ok bool) {
	m := allocatablePattern.FindStringSubmatch(evidence)
	if m == nil {
		return 0, 0, false
	}
	mem, memErr := strconv.ParseInt(m[1], 10, 64)
	cpu, cpuErr := strconv.ParseInt(m[2], 10, 64)
	if memErr != nil || cpuErr != nil || mem <= 0 || cpu <= 0 {
		return 0, 0, false
	}
	return mem, cpu, true
}

// parseTopMemory pulls a container's memory figure out of
// `kubectl top pod --containers` output, in mebibytes. It declines on a zero
// sample: a container OOM-killed at startup may never report one, and
// inventing a number would produce a patch that cannot work.
func parseTopMemory(text, container string) (int64, bool) {
	for _, line := range strings.Split(text, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 || fields[1] != container {
			continue
		}
		value := strings.TrimSuffix(fields[3], "Mi")
		mi, err := strconv.ParseInt(value, 10, 64)
		if err != nil || mi <= 0 {
			return 0, false
		}
		return mi, true
	}
	return 0, false
}

// parseTopCPU pulls a container's CPU figure, in millicores, out of the same
// `kubectl top pod --containers` output (the CPU(cores) column, e.g. "2m").
// Callers floor the result — a zero CPU sample is ordinary, not a sign the
// metric is missing.
func parseTopCPU(text, container string) (int64, bool) {
	for _, line := range strings.Split(text, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 || fields[1] != container {
			continue
		}
		m, err := strconv.ParseInt(strings.TrimSuffix(fields[2], "m"), 10, 64)
		if err != nil || m < 0 {
			return 0, false
		}
		return m, true
	}
	return 0, false
}

// roundUpMi rounds to the next 16Mi so proposed values read like something a
// human would choose.
func roundUpMi(mi int64) int64 {
	const step = 16
	if mi <= 0 {
		return step
	}
	return ((mi + step - 1) / step) * step
}

// declaredOOMDemand accepts only a narrowly understood allocation in the same
// pod/container diagnosed as OOMKilled. It never executes workload commands.
func declaredOOMDemand(ctx context.Context, r kubeReader, f analyzer.Finding, container string) (int64, bool) {
	if r == nil || f.Status != "OOMKilled" || f.PodName == "" || f.Namespace == "" {
		return 0, false
	}
	raw, err := r.GetResource(ctx, f.Namespace, "pod/"+f.PodName)
	if err != nil {
		return 0, false
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return 0, false
	}
	var pod kube.Pod
	if json.Unmarshal(data, &pod) != nil || pod.Metadata.Name != f.PodName || pod.Metadata.Namespace != f.Namespace {
		return 0, false
	}
	oom := false
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.Name == container && !cs.Ready && (cs.State["terminated"].IsOOMKilled() || cs.LastState["terminated"].IsOOMKilled()) {
			oom = true
		}
	}
	if !oom {
		return 0, false
	}
	for _, c := range pod.Spec.Containers {
		if c.Name != container || c.Image != EvidenceValue(f, "Container image "+container) {
			continue
		}
		bytes, ok := stressDemand(c)
		if !ok {
			bytes, ok = memoryBackedDDDemand(c, raw)
		}
		if !ok {
			return 0, false
		}
		limit, ok := memoryLimitBytes(c.Resources.Limits["memory"])
		if !ok || bytes <= limit {
			return 0, false
		}
		return (bytes + 1024*1024 - 1) / (1024 * 1024), true
	}
	return 0, false
}

// stress has per-worker --vm-bytes allocation. Its K/M/G suffixes are binary
// (upstream src/stress.c atoll_b). No percentages, defaults or shell expansion.
func stressDemand(c kube.Container) (int64, bool) {
	args := c.Args
	if len(c.Command) > 0 {
		if path.Base(c.Command[0]) != "stress" {
			return 0, false
		}
		args = append(append([]string{}, c.Command[1:]...), args...)
	} else {
		image := strings.SplitN(strings.SplitN(c.Image, "@", 2)[0], ":", 2)[0]
		image = strings.TrimPrefix(image, "docker.io/")
		if image != "polinux/stress" {
			return 0, false
		}
	}
	values := map[string]string{}
	for i := 0; i < len(args); i++ {
		flag := args[i]
		if flag == "-m" {
			flag = "--vm"
		}
		switch flag {
		case "--vm", "--vm-bytes", "--vm-hang":
			if _, exists := values[flag]; exists || i+1 >= len(args) {
				return 0, false
			}
			i++
			values[flag] = args[i]
		case "--vm-keep":
			if _, exists := values[flag]; exists {
				return 0, false
			}
			values[flag] = "true"
		default:
			return 0, false
		}
	}
	workers, err := strconv.ParseInt(values["--vm"], 10, 64)
	if err != nil || workers <= 0 {
		return 0, false
	}
	if hang, exists := values["--vm-hang"]; exists {
		n, e := strconv.ParseInt(hang, 10, 64)
		if e != nil || n < 0 {
			return 0, false
		}
	}
	if !stressBytesPattern.MatchString(values["--vm-bytes"]) {
		return 0, false
	}
	bytes, ok := allocationBytes(values["--vm-bytes"])
	if !ok || bytes > math.MaxInt64/workers {
		return 0, false
	}
	total := bytes * workers
	// Leave arithmetic room for rounding and 1.5x headroom.
	if total > math.MaxInt64/2 {
		return 0, false
	}
	return total, true
}

var stressBytesPattern = regexp.MustCompile(`^[0-9]+[kKmMgG]?$`)
var memoryLimitPattern = regexp.MustCompile(`^[0-9]+(Ki|Mi|Gi)?$`)

func memoryLimitBytes(value string) (int64, bool) {
	if !memoryLimitPattern.MatchString(value) {
		return 0, false
	}
	return allocationBytes(value)
}

var allocationPattern = regexp.MustCompile(`^([0-9]+)(Ki|Mi|Gi|K|M|G|k|m|g)?$`)

func allocationBytes(value string) (int64, bool) {
	m := allocationPattern.FindStringSubmatch(value)
	if m == nil {
		return 0, false
	}
	n, err := strconv.ParseInt(m[1], 10, 64)
	if err != nil || n <= 0 {
		return 0, false
	}
	multiplier := int64(1)
	switch strings.ToLower(m[2]) {
	case "k", "ki":
		multiplier = 1024
	case "m", "mi":
		multiplier = 1024 * 1024
	case "g", "gi":
		multiplier = 1024 * 1024 * 1024
	}
	if n > math.MaxInt64/multiplier {
		return 0, false
	}
	return n * multiplier, true
}

// A dd write only establishes memory demand when its output is on the pod's
// memory emptyDir. Disk-backed writes and arbitrary shell scripts decline.
func memoryBackedDDDemand(c kube.Container, raw map[string]any) (int64, bool) {
	tokens := append(append([]string{}, c.Command...), c.Args...)
	if len(tokens) == 3 && (tokens[0] == "sh" || tokens[0] == "/bin/sh") && tokens[1] == "-c" {
		script := tokens[2]
		if strings.ContainsAny(script, "$`\\\"'\n|&<>") {
			return 0, false
		}
		parts := strings.Split(script, ";")
		if len(parts) > 2 {
			return 0, false
		}
		if len(parts) == 2 {
			tail := strings.Fields(parts[1])
			if len(tail) != 2 || tail[0] != "sleep" {
				return 0, false
			}
			duration, err := strconv.ParseInt(tail[1], 10, 64)
			if err != nil || duration <= 0 {
				return 0, false
			}
		}
		tokens = strings.Fields(parts[0])
	}
	if len(tokens) != 5 || tokens[0] != "dd" {
		return 0, false
	}
	opts := map[string]string{}
	for _, token := range tokens[1:] {
		kv := strings.SplitN(token, "=", 2)
		if len(kv) != 2 {
			return 0, false
		}
		if _, exists := opts[kv[0]]; exists {
			return 0, false
		}
		opts[kv[0]] = kv[1]
	}
	if opts["if"] != "/dev/zero" || !path.IsAbs(opts["of"]) {
		return 0, false
	}
	block, ok := allocationBytes(opts["bs"])
	if !ok || !stressBytesPattern.MatchString(opts["bs"]) {
		return 0, false
	}
	count, err := strconv.ParseInt(opts["count"], 10, 64)
	if err != nil || count <= 0 || block > math.MaxInt64/count {
		return 0, false
	}
	bytes := block * count
	if bytes > math.MaxInt64/2 {
		return 0, false
	}
	dest := path.Clean(opts["of"])
	mountName, mountPath := "", ""
	for _, mount := range c.VolumeMounts {
		mpath, _ := mount["mountPath"].(string)
		name, _ := mount["name"].(string)
		if mpath == "" || name == "" {
			continue
		}
		mpath = path.Clean(mpath)
		if strings.HasPrefix(dest, strings.TrimSuffix(mpath, "/")+"/") && len(mpath) >= len(mountPath) {
			if len(mpath) == len(mountPath) || mount["readOnly"] == true || mount["subPath"] != nil || mount["subPathExpr"] != nil {
				return 0, false
			}
			mountName, mountPath = name, mpath
		}
	}
	spec, _ := raw["spec"].(map[string]any)
	volumes, _ := spec["volumes"].([]any)
	for _, entry := range volumes {
		volume, _ := entry.(map[string]any)
		if volume["name"] != mountName {
			continue
		}
		empty, _ := volume["emptyDir"].(map[string]any)
		if empty["medium"] != "Memory" {
			return 0, false
		}
		if size, exists := empty["sizeLimit"]; exists {
			limit, ok := memoryLimitBytes(fmt.Sprint(size))
			if !ok || bytes > limit {
				return 0, false
			}
		}
		return bytes, true
	}
	return 0, false
}
