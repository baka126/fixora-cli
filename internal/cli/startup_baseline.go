package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"github.com/fixora/kubectl-fixora/internal/analyzer"
	"github.com/fixora/kubectl-fixora/internal/fix"
	"github.com/fixora/kubectl-fixora/internal/kube"
	"gopkg.in/yaml.v3"
	"k8s.io/apimachinery/pkg/util/validation"
)

// withStartupBaseline accepts startup instructions only from an explicit
// operator-supplied manifest that matches the live workload and image.
func withStartupBaseline(ctx context.Context, reader kube.Reader, baselinePath string, finding analyzer.Finding, plan fix.Plan) (fix.Plan, error) {
	if baselinePath == "" || reader == nil {
		return plan, errors.New("startup restoration requires an explicit baseline file and cluster reader")
	}
	if finding.ResourceKind != "Deployment" || finding.Status != "CrashLoopBackOff" || plan.Strategy != "runtime" {
		return plan, errors.New("startup baseline applies only to a crashing Deployment runtime plan")
	}
	if len(validation.IsDNS1123Subdomain(finding.ResourceName)) != 0 || len(validation.IsDNS1123Label(finding.Namespace)) != 0 {
		return plan, errors.New("startup baseline requires an exact valid workload name and namespace")
	}
	if plan.Resource != "Deployment/"+finding.ResourceName || plan.Namespace != finding.Namespace {
		return plan, errors.New("startup baseline plan identity differs from finding")
	}
	for _, reason := range plan.BlockedReasons {
		if strings.Contains(reason, "GitOps") {
			return plan, errors.New("startup restoration requires source-level delivery for GitOps-managed workloads")
		}
	}
	file, err := os.Open(baselinePath)
	if err != nil {
		return plan, fmt.Errorf("read startup baseline: %w", err)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 1024*1024+1))
	if err != nil {
		return plan, err
	}
	if len(data) > 1024*1024 {
		return plan, errors.New("startup baseline exceeds 1MiB")
	}
	decoder := yaml.NewDecoder(strings.NewReader(string(data)))
	var baseline map[string]any
	if err := decoder.Decode(&baseline); err != nil {
		return plan, fmt.Errorf("parse startup baseline: %w", err)
	}
	var additional any
	if err := decoder.Decode(&additional); err != io.EOF {
		return plan, errors.New("startup baseline must contain exactly one YAML document")
	}
	if containsStartupSecretReference(baseline) {
		return plan, errors.New("startup baseline must not reference Secrets")
	}
	bc, err := startupContainer(baseline, finding)
	if err != nil {
		return plan, fmt.Errorf("startup baseline: %w", err)
	}
	current, err := reader.GetResource(ctx, finding.Namespace, "deployment/"+finding.ResourceName)
	if err != nil {
		return plan, fmt.Errorf("read live startup identity: %w", err)
	}
	cc, err := startupContainer(current, finding)
	if err != nil {
		return plan, fmt.Errorf("live startup identity: %w", err)
	}
	name, _ := bc["name"].(string)
	image, _ := bc["image"].(string)
	if name == "" || image == "" || name != cc["name"] || image != cc["image"] || len(validation.IsDNS1123Label(name)) != 0 {
		return plan, errors.New("startup baseline container name and image must exactly match the live Deployment")
	}
	command, ok := startupStrings(bc["command"])
	if !ok || len(command) == 0 || strings.TrimSpace(command[0]) == "" {
		return plan, errors.New("startup baseline requires a nonempty command array")
	}
	args := []string{}
	if raw, exists := bc["args"]; exists {
		args, ok = startupStrings(raw)
		if !ok {
			return plan, errors.New("startup baseline args must be a string array")
		}
	}
	for _, key := range []string{"command", "args"} {
		if raw, exists := cc[key]; exists {
			if _, valid := startupStrings(raw); !valid {
				return plan, errors.New("live startup fields are malformed")
			}
		}
	}
	currentCommand, _ := startupStrings(cc["command"])
	currentArgs, _ := startupStrings(cc["args"])
	if slices.Equal(command, currentCommand) && slices.Equal(args, currentArgs) {
		return plan, errors.New("startup baseline already matches the live command and args; no restoration is needed")
	}
	patchContainer := map[string]any{"name": name, "command": command, "args": args}
	patch := map[string]any{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": map[string]any{"name": finding.ResourceName, "namespace": finding.Namespace}, "spec": map[string]any{"template": map[string]any{"spec": map[string]any{"containers": []any{patchContainer}}}}}
	encoded, err := yaml.Marshal(patch)
	if err != nil {
		return plan, err
	}
	plan.PatchTemplate = string(encoded)
	plan.Strategy = "restore-command"
	plan.Confidence = 95
	plan.Safe = true
	plan.Risk = "low"
	plan.CanApply = !plan.Recovered && !finding.Recovered
	plan.ApplyEligible = plan.CanApply
	plan.BlockedReasons = nil
	if !plan.CanApply {
		plan.BlockedReasons = []string{"Workload is already recovered; startup restoration requires review."}
	}
	plan.Patches = []fix.Patch{{Type: "strategic-merge", Target: plan.Resource, Description: "Restore startup command and args from the operator-supplied matching baseline.", Preview: plan.PatchTemplate}}
	plan.Guardrails = append(append([]string{}, plan.Guardrails...), "operator-startup-baseline", "shadow-required")
	plan.Warnings = append(append([]string{}, plan.Warnings...), "The operator supplied this startup baseline; verify its behavior in shadow before production delivery.")
	return plan, nil
}

func startupContainer(obj map[string]any, f analyzer.Finding) (map[string]any, error) {
	meta, _ := obj["metadata"].(map[string]any)
	if obj["apiVersion"] != "apps/v1" || obj["kind"] != "Deployment" || meta["name"] != f.ResourceName || meta["namespace"] != f.Namespace {
		return nil, errors.New("Deployment kind, name, and namespace must match the finding")
	}
	spec, _ := obj["spec"].(map[string]any)
	template, _ := spec["template"].(map[string]any)
	podSpec, _ := template["spec"].(map[string]any)
	containers, _ := podSpec["containers"].([]any)
	if len(containers) != 1 {
		return nil, errors.New("exactly one container is required")
	}
	container, ok := containers[0].(map[string]any)
	if !ok {
		return nil, errors.New("container must be an object")
	}
	return container, nil
}
func startupStrings(raw any) ([]string, bool) {
	values, ok := raw.([]any)
	if !ok {
		return nil, false
	}
	result := make([]string, 0, len(values))
	for _, raw := range values {
		value, ok := raw.(string)
		if !ok || strings.ContainsRune(value, 0) {
			return nil, false
		}
		result = append(result, value)
	}
	return result, true
}
func containsStartupSecretReference(raw any) bool {
	switch value := raw.(type) {
	case map[string]any:
		for key, item := range value {
			switch key {
			case "secretKeyRef", "secretRef", "secret", "imagePullSecrets":
				return true
			}
			if containsStartupSecretReference(item) {
				return true
			}
		}
	case []any:
		for _, item := range value {
			if containsStartupSecretReference(item) {
				return true
			}
		}
	}
	return false
}
