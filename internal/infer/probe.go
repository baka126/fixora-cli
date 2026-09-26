package infer

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/fixora/kubectl-fixora/internal/analyzer"
	"github.com/fixora/kubectl-fixora/internal/fix"
)

func init() { register(probeInferrer{}) }

type probeInferrer struct{}

func (probeInferrer) Name() string { return "probe" }

func (probeInferrer) Handles(plan fix.Plan) bool {
	return strings.EqualFold(plan.Strategy, "probe")
}

// Infer proposes the container's only declared port when the readiness probe
// targets a different numeric port. It declines on anything ambiguous or
// already-correct: zero or several declared ports, a probe that already
// matches, a named probe port (a resolving name is not a misconfiguration), or
// a declared value that is not a plain positive integer — that value is
// substituted textually into the patch, so a stray newline would inject YAML.
func (probeInferrer) Infer(_ context.Context, _ kubeReader, f analyzer.Finding, _ fix.Plan) (Result, bool, error) {
	container := ContainerName(f)
	if container == "" {
		return Result{}, false, nil
	}
	declared := splitPorts(EvidenceValue(f, "Container ports"))
	if len(declared) != 1 {
		return Result{}, false, nil
	}
	probePort := strings.TrimSpace(EvidenceValue(f, "Readiness probe port"))
	if !isNumericPort(probePort) {
		// Empty, or a named port like "http": nothing numeric to correct.
		return Result{}, false, nil
	}
	target := declared[0]
	if !isNumericPort(target) || target == probePort {
		return Result{}, false, nil
	}
	return Result{
		Options:   fix.ConcreteOptions{Container: container, ProbePort: target},
		Guardrail: "inferred-probe-from-containerport",
		Warning: fmt.Sprintf(
			"Container %s declares only port %s but its readiness probe targets %s; probe port changed to %s — confirm the probe path is served there.",
			container, target, probePort, target),
	}, true, nil
}

// splitPorts turns a comma-separated evidence value into a trimmed list.
func splitPorts(value string) []string {
	out := []string{}
	for _, part := range strings.Split(value, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

// isNumericPort reports whether s is a plain positive integer string — all
// ASCII digits, no sign, no whitespace, in the valid port range.
func isNumericPort(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	n, err := strconv.Atoi(s)
	return err == nil && n > 0 && n <= 65535
}
