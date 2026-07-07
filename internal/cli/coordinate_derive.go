package cli

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/fixora/kubectl-fixora/internal/analyzer"
	"github.com/fixora/kubectl-fixora/internal/coordinate"
	"github.com/fixora/kubectl-fixora/internal/kube"
	"github.com/fixora/kubectl-fixora/internal/termui"
)

// References is the set of resources a workload's pod template actually
// references, plus the pod-template labels (for Service selector matching).
type References struct {
	ConfigMaps []string
	Secrets    []string
	PVCs       []string
	PodLabels  map[string]string
}

func asMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}
func mapAt(m map[string]any, key string) map[string]any { return asMap(m[key]) }
func sliceAt(m map[string]any, key string) []any {
	s, _ := m[key].([]any)
	return s
}
func strAt(m map[string]any, key string) string {
	s, _ := m[key].(string)
	return s
}
func appendUniq(list []string, v string) []string {
	if v == "" {
		return list
	}
	for _, e := range list {
		if e == v {
			return list
		}
	}
	return append(list, v)
}

// extractReferences walks the root workload's pod template for the ConfigMaps,
// Secrets, and PVCs it references, plus its pod-template labels. Read-only, pure.
// ponytail: covers the standard workload kinds; exotic/unknown kinds yield no refs.
func extractReferences(root map[string]any) References {
	refs := References{PodLabels: map[string]string{}}
	spec := mapAt(root, "spec")
	var tmpl map[string]any
	switch strings.ToLower(strAt(root, "kind")) {
	case "cronjob":
		tmpl = mapAt(mapAt(mapAt(spec, "jobTemplate"), "spec"), "template")
	case "pod":
		tmpl = root
	default:
		tmpl = mapAt(spec, "template")
	}
	for k, v := range mapAt(mapAt(tmpl, "metadata"), "labels") {
		if s, ok := v.(string); ok {
			refs.PodLabels[k] = s
		}
	}
	podSpec := mapAt(tmpl, "spec")

	for _, ips := range sliceAt(podSpec, "imagePullSecrets") {
		refs.Secrets = appendUniq(refs.Secrets, strAt(asMap(ips), "name"))
	}
	containers := append(append([]any{}, sliceAt(podSpec, "initContainers")...), sliceAt(podSpec, "containers")...)
	for _, c := range containers {
		cm := asMap(c)
		for _, ef := range sliceAt(cm, "envFrom") {
			efm := asMap(ef)
			refs.ConfigMaps = appendUniq(refs.ConfigMaps, strAt(mapAt(efm, "configMapRef"), "name"))
			refs.Secrets = appendUniq(refs.Secrets, strAt(mapAt(efm, "secretRef"), "name"))
		}
		for _, e := range sliceAt(cm, "env") {
			vf := mapAt(asMap(e), "valueFrom")
			refs.ConfigMaps = appendUniq(refs.ConfigMaps, strAt(mapAt(vf, "configMapKeyRef"), "name"))
			refs.Secrets = appendUniq(refs.Secrets, strAt(mapAt(vf, "secretKeyRef"), "name"))
		}
	}
	for _, v := range sliceAt(podSpec, "volumes") {
		vm := asMap(v)
		refs.ConfigMaps = appendUniq(refs.ConfigMaps, strAt(mapAt(vm, "configMap"), "name"))
		refs.Secrets = appendUniq(refs.Secrets, strAt(mapAt(vm, "secret"), "secretName"))
		refs.PVCs = appendUniq(refs.PVCs, strAt(mapAt(vm, "persistentVolumeClaim"), "claimName"))
	}
	return refs
}

// matchingServices returns the names of services whose (non-empty) selector is
// a subset of podLabels.
func matchingServices(services []map[string]any, podLabels map[string]string) []string {
	var out []string
	for _, svc := range services {
		sel := mapAt(mapAt(svc, "spec"), "selector")
		if len(sel) == 0 {
			continue
		}
		match := true
		for k, v := range sel {
			if vs, _ := v.(string); podLabels[k] != vs {
				match = false
				break
			}
		}
		if match {
			out = appendUniq(out, strAt(mapAt(svc, "metadata"), "name"))
		}
	}
	return out
}

// assembleRefs orders the derived refs dependency-first: config/secret/pvc,
// then the root workload, then services.
func assembleRefs(refs References, services []string, rootRef string) []string {
	var out []string
	for _, c := range refs.ConfigMaps {
		out = append(out, "ConfigMap/"+c)
	}
	for _, s := range refs.Secrets {
		out = append(out, "Secret/"+s)
	}
	for _, p := range refs.PVCs {
		out = append(out, "PersistentVolumeClaim/"+p)
	}
	out = append(out, rootRef)
	for _, s := range services {
		out = append(out, "Service/"+s)
	}
	return out
}

// inclusionReason explains, from a derived ref's kind prefix, why it was added
// to the coordinated set — so the operator sees what each mutation touches and
// why before confirming.
func inclusionReason(ref, rootRef string) string {
	if ref == rootRef {
		return "root workload"
	}
	switch {
	case strings.HasPrefix(ref, "ConfigMap/"):
		return "referenced ConfigMap"
	case strings.HasPrefix(ref, "Secret/"):
		return "referenced Secret"
	case strings.HasPrefix(ref, "PersistentVolumeClaim/"):
		return "mounted PVC"
	case strings.HasPrefix(ref, "Service/"):
		return "selector-matched Service"
	default:
		return "referenced by the root workload"
	}
}

// filterApplyEligible keeps only steps whose plan is apply-eligible, preserving
// order. Healthy/unfixable references drop out of the coordinated set.
func filterApplyEligible(steps []coordinate.Step) []coordinate.Step {
	out := make([]coordinate.Step, 0, len(steps))
	for _, s := range steps {
		if s.Plan.ApplyEligible {
			out = append(out, s)
		}
	}
	return out
}

// deriveRelatedRefs builds the ordered related-resource ref list for a root
// workload by walking its pod template (read-only).
func deriveRelatedRefs(ctx context.Context, r kube.Reader, rootRef, namespace string) ([]string, error) {
	root, err := r.GetResource(ctx, namespace, rootRef)
	if err != nil {
		return nil, err
	}
	refs := extractReferences(root)
	services, _ := r.GetResourceItems(ctx, namespace, false, "services")
	return assembleRefs(refs, matchingServices(services, refs.PodLabels), rootRef), nil
}

// runCoordinateFrom derives the related set from a root ref, keeps only the
// apply-eligible steps, and runs the existing coordinate saga on them. Returns
// the process exit code.
func runCoordinateFrom(ctx context.Context, stdout, stderr io.Writer, opts options, a analyzer.Analyzer, k kube.Kubectl, rootRef string) int {
	rootFinding, err := a.AnalyzeResource(ctx, rootRef)
	if err != nil {
		fmt.Fprintf(stderr, "error: analyze %s: %v\n", rootRef, err)
		return 1
	}
	refs, err := deriveRelatedRefs(ctx, k, rootRef, rootFinding.Namespace)
	if err != nil {
		fmt.Fprintf(stderr, "error: derive related resources: %v\n", err)
		return 1
	}
	steps, err := buildCoordinateSteps(ctx, a, opts, refs)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	eligible := filterApplyEligible(steps)

	fmt.Fprintln(stdout, "Derived coordinated fix set")
	fmt.Fprintln(stdout, "===========================")
	for _, s := range eligible {
		fmt.Fprintf(stdout, "- %s (%s)\n", s.Ref, inclusionReason(s.Ref, rootRef))
	}
	if len(eligible) < 2 {
		fmt.Fprintf(stdout, "\nFewer than two resources need a coordinated fix. Use `fix %s` for a single-resource fix.\n", rootRef)
		return 0
	}

	in := inputFor(opts)
	confirmApply := func() bool {
		if opts.yes {
			return true
		}
		return termui.ConfirmRollback(fmt.Sprintf("apply %d coordinated changes", len(eligible)), in, stderr)
	}
	confirmRollback := func() bool {
		if opts.yes {
			return false
		}
		return termui.ConfirmRollback("roll back the already-applied changes", in, stderr)
	}
	deps := coordinateDeps{k: k, timeout: opts.rolloutTimeout}
	return runCoordinateSteps(ctx, stdout, stderr, eligible, deps, confirmApply, confirmRollback)
}
