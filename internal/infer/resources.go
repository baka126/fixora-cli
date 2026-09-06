package infer

import (
	"context"
	"fmt"
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

func (resourcesInferrer) Infer(_ context.Context, _ kubeReader, f analyzer.Finding, _ fix.Plan) (Result, bool, error) {
	container := ContainerName(f)
	if container == "" {
		return Result{}, false, nil
	}
	// OOMKilled: size from what the container actually used.
	top := EvidenceValue(f, "Metrics pod containers")
	if observed, ok := parseTopMemory(top, container); ok {
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
	return Result{}, false, nil
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
