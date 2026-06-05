package ops

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/fixora/kubectl-fixora/internal/analyzer"
	"github.com/fixora/kubectl-fixora/internal/fix"
	"github.com/fixora/kubectl-fixora/internal/kube"
)

type Health struct {
	Namespace           string                  `json:"namespace,omitempty"`
	Findings            int                     `json:"findings"`
	HighSeverity        int                     `json:"highSeverity"`
	MediumSeverity      int                     `json:"mediumSeverity"`
	LowSeverity         int                     `json:"lowSeverity"`
	ServicesNoEndpoints []string                `json:"servicesNoEndpoints,omitempty"`
	Skipped             []analyzer.SkippedCheck `json:"skipped,omitempty"`
	TopFindings         []string                `json:"topFindings,omitempty"`
}

type Readiness struct {
	Resource  string   `json:"resource"`
	Namespace string   `json:"namespace"`
	Score     int      `json:"score"`
	Ready     bool     `json:"ready"`
	Checks    []string `json:"checks"`
	Missing   []string `json:"missing,omitempty"`
	Warnings  []string `json:"warnings,omitempty"`
}

type Change struct {
	Resource  string `json:"resource"`
	Namespace string `json:"namespace,omitempty"`
	Signal    string `json:"signal"`
	Value     string `json:"value"`
}

type Rollback struct {
	Resource  string   `json:"resource"`
	Preview   bool     `json:"preview"`
	Command   string   `json:"command,omitempty"`
	Binary    string   `json:"binary,omitempty"`
	Args      []string `json:"args,omitempty"`
	Namespace string   `json:"namespace,omitempty"`
	Warnings  []string `json:"warnings,omitempty"`
}

type Preflight struct {
	Path     string                `json:"path"`
	Valid    bool                  `json:"valid"`
	Lint     []analyzer.LintResult `json:"lint"`
	DryRun   string                `json:"dryRun"`
	Diff     string                `json:"diff,omitempty"`
	Warnings []string              `json:"warnings,omitempty"`
}

func BuildRunbook(f analyzer.Finding, plan fix.Plan) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Fixora Runbook\n\n")
	fmt.Fprintf(&b, "- Resource: `%s/%s`\n", f.ResourceKind, f.ResourceName)
	fmt.Fprintf(&b, "- Namespace: `%s`\n", f.Namespace)
	fmt.Fprintf(&b, "- Status: `%s`\n", f.Status)
	fmt.Fprintf(&b, "- Severity: `%s`\n", f.Severity)
	fmt.Fprintf(&b, "- Confidence: `%d`\n\n", plan.Confidence)
	fmt.Fprintf(&b, "## Impact\n\n")
	fmt.Fprintf(&b, "- Category: `%s`\n", f.Category)
	if f.PodName != "" {
		fmt.Fprintf(&b, "- Evidence pod: `%s`\n", f.PodName)
	}
	fmt.Fprintf(&b, "\n## Verify\n\n")
	fmt.Fprintf(&b, "```sh\nkubectl get %s/%s -n %s -o wide\nkubectl describe %s/%s -n %s\n```\n\n", strings.ToLower(f.ResourceKind), f.ResourceName, f.Namespace, strings.ToLower(f.ResourceKind), f.ResourceName, f.Namespace)
	if f.PodName != "" {
		fmt.Fprintf(&b, "```sh\nkubectl logs pod/%s -n %s --tail=120\nkubectl get events -n %s --sort-by=.lastTimestamp\n```\n\n", f.PodName, f.Namespace, f.Namespace)
	}
	fmt.Fprintf(&b, "## Evidence\n\n")
	for _, ev := range f.Evidence {
		fmt.Fprintf(&b, "- **%s:** %s\n", ev.Label, trim(ev.Value, 300))
	}
	fmt.Fprintf(&b, "\n## Safe Fix Path\n\n")
	for _, step := range plan.Steps {
		fmt.Fprintf(&b, "- %s\n", step)
	}
	if plan.PatchTemplate != "" {
		fmt.Fprintf(&b, "\n## Patch Template\n\n```yaml\n%s\n```\n", plan.PatchTemplate)
	}
	if plan.RollbackCommand != "" {
		fmt.Fprintf(&b, "\n## Rollback\n\n```sh\n%s\n```\n", plan.RollbackCommand)
	}
	if len(plan.Warnings) > 0 {
		fmt.Fprintf(&b, "\n## Warnings\n\n")
		for _, warning := range plan.Warnings {
			fmt.Fprintf(&b, "- %s\n", warning)
		}
	}
	return b.String()
}

func FixReadiness(f analyzer.Finding, plan fix.Plan) Readiness {
	r := Readiness{Resource: f.ResourceKind + "/" + f.ResourceName, Namespace: f.Namespace}
	add := func(ok bool, check string) {
		if ok {
			r.Score += 15
			r.Checks = append(r.Checks, check)
		} else {
			r.Missing = append(r.Missing, check)
		}
	}
	add(len(f.Evidence) > 0, "evidence collected")
	add(len(f.Logs) > 0 || f.PodName == "", "logs available or not required")
	add(f.ResourceKind != "" && f.ResourceName != "", "resource identity known")
	add(plan.RollbackCommand != "", "rollback command available")
	add(!strings.Contains(plan.PatchTemplate, "TODO_"), "patch is concrete")
	add(plan.CanApply, "patch marked apply-ready")
	if f.GitOps.TargetAdvice != "" {
		r.Warnings = append(r.Warnings, "GitOps-managed workload requires source-level change")
	} else {
		r.Score += 10
		r.Checks = append(r.Checks, "no GitOps blocker detected")
	}
	if r.Score > 100 {
		r.Score = 100
	}
	r.Ready = r.Score >= 75 && plan.CanApply
	return r
}

func BuildHealth(ctx context.Context, k kube.Kubectl, report analyzer.ScanReport, namespace string) Health {
	h := Health{Namespace: namespace, Findings: report.Summary.Findings, HighSeverity: report.Summary.HighSeverity, MediumSeverity: report.Summary.MediumSeverity, LowSeverity: report.Summary.LowSeverity, Skipped: report.Skipped}
	for i, finding := range report.Findings {
		if i >= 5 {
			break
		}
		h.TopFindings = append(h.TopFindings, finding.Severity+": "+finding.ResourceKind+"/"+finding.ResourceName+" "+finding.Status)
	}
	services, err := k.GetResourceItems(ctx, namespace, namespace == "", "services")
	if err == nil {
		endpoints, _ := k.GetResourceItems(ctx, namespace, namespace == "", "endpoints")
		endpointNames := map[string]bool{}
		for _, ep := range endpoints {
			endpointNames[metaNamespaceName(ep)] = hasEndpointSubsets(ep)
		}
		for _, svc := range services {
			key := metaNamespaceName(svc)
			if !endpointNames[key] {
				h.ServicesNoEndpoints = append(h.ServicesNoEndpoints, key)
			}
		}
		sort.Strings(h.ServicesNoEndpoints)
	}
	return h
}

func DetectChanges(ctx context.Context, k kube.Kubectl, namespace, resource string) ([]Change, error) {
	obj, err := k.GetResource(ctx, namespace, resource)
	if err != nil {
		return nil, err
	}
	changes := []Change{}
	ns := mapString(obj, "metadata", "namespace")
	if ns == "" {
		ns = namespace
	}
	name := mapString(obj, "metadata", "name")
	changes = append(changes, Change{Resource: resource, Namespace: ns, Signal: "resourceVersion", Value: mapString(obj, "metadata", "resourceVersion")})
	changes = append(changes, Change{Resource: resource, Namespace: ns, Signal: "generation", Value: mapString(obj, "metadata", "generation")})
	changes = append(changes, Change{Resource: resource, Namespace: ns, Signal: "observedGeneration", Value: mapString(obj, "status", "observedGeneration")})
	changes = append(changes, Change{Resource: resource, Namespace: ns, Signal: "created", Value: mapString(obj, "metadata", "creationTimestamp")})
	annotations, _ := nestedMap(obj, "metadata", "annotations")
	for key, value := range annotations {
		if strings.Contains(strings.ToLower(key), "revision") || strings.Contains(strings.ToLower(key), "checksum") || strings.Contains(strings.ToLower(key), "image") {
			changes = append(changes, Change{Resource: resource, Namespace: ns, Signal: key, Value: fmt.Sprint(value)})
		}
	}
	if strings.HasPrefix(strings.ToLower(resource), "deployment/") {
		rs, _ := k.GetResourceItems(ctx, namespace, false, "replicasets")
		for _, item := range rs {
			for _, owner := range ownerRefs(item) {
				if owner == "Deployment/"+name {
					changes = append(changes, Change{Resource: "ReplicaSet/" + mapString(item, "metadata", "name"), Namespace: ns, Signal: "revision", Value: mapString(item, "metadata", "annotations", "deployment.kubernetes.io/revision")})
				}
			}
		}
	}
	events, _ := k.GetEvents(ctx, namespace, "")
	for _, event := range events {
		if strings.Contains(event.Message, name) || strings.Contains(event.Metadata.Name, name) {
			changes = append(changes, Change{Resource: resource, Namespace: ns, Signal: "event/" + event.Reason, Value: trim(event.Message, 220)})
		}
	}
	for _, image := range collectImages(obj) {
		changes = append(changes, Change{Resource: resource, Namespace: ns, Signal: "image", Value: image})
	}
	return changes, nil
}

func BuildRollback(f analyzer.Finding, plan fix.Plan, apply bool) Rollback {
	r := Rollback{Resource: f.ResourceKind + "/" + f.ResourceName, Preview: !apply, Command: plan.RollbackCommand}
	r.Binary, r.Args = StructuredRollbackCommand(plan.RollbackCommand)
	r.Namespace = f.Namespace
	if r.Command == "" {
		r.Warnings = append(r.Warnings, "No deterministic rollback command found.")
	} else if r.Binary == "" {
		r.Warnings = append(r.Warnings, "Rollback command is advisory only and cannot be executed safely.")
	}
	if f.GitOps.TargetAdvice != "" {
		r.Warnings = append(r.Warnings, "GitOps-managed workload may require reverting source and reconciling the controller.")
	}
	return r
}

func StructuredRollbackCommand(command string) (string, []string) {
	var fields []string
	var current strings.Builder
	inSingle := false
	inDouble := false
	escape := false

	for _, r := range strings.TrimSpace(command) {
		if escape {
			current.WriteRune(r)
			escape = false
			continue
		}
		if r == '\\' {
			escape = true
			continue
		}
		if r == '\'' && !inDouble {
			inSingle = !inSingle
			continue
		}
		if r == '"' && !inSingle {
			inDouble = !inDouble
			continue
		}
		if (r == ' ' || r == '\t') && !inSingle && !inDouble {
			if current.Len() > 0 {
				fields = append(fields, current.String())
				current.Reset()
			}
			continue
		}
		current.WriteRune(r)
	}
	if current.Len() > 0 {
		fields = append(fields, current.String())
	}
	if len(fields) == 0 {
		return "", nil
	}
	switch fields[0] {
	case "kubectl", "helm":
	default:
		return "", nil
	}
	for _, field := range fields {
		if strings.ContainsAny(field, ";&|`$><(){}[]") {
			return "", nil
		}
	}
	return fields[0], fields[1:]
}

func RunPreflight(ctx context.Context, k kube.Kubectl, file string) Preflight {
	p := Preflight{Path: file}
	lint, err := analyzer.Lint([]string{file})
	if err != nil {
		p.Warnings = append(p.Warnings, err.Error())
	} else {
		p.Lint = lint
	}
	if err := k.DryRunApply(ctx, file); err != nil {
		p.DryRun = "failed: " + err.Error()
		p.Valid = false
		return p
	}
	p.DryRun = "ok"
	if diff, err := k.Diff(ctx, file); err == nil {
		p.Diff = trim(diff, 4000)
	} else {
		p.Warnings = append(p.Warnings, "kubectl diff unavailable: "+err.Error())
	}
	p.Valid = !hasHighLint(lint)
	return p
}

func WriteSourcePatch(repoPath, outFile string, plan fix.Plan) (string, error) {
	if repoPath == "" {
		return "", fmt.Errorf("--source-patch requires --repo")
	}
	if outFile == "" {
		outFile = "fixora-source-patch.yaml"
	}
	target := outFile
	if !filepath.IsAbs(target) {
		target = filepath.Join(repoPath, outFile)
	}
	if err := os.WriteFile(target, []byte(plan.PatchYAML()), 0o600); err != nil {
		return "", err
	}
	return target, nil
}

func collectImages(obj map[string]any) []string {
	images := map[string]bool{}
	var walk func(any)
	walk = func(value any) {
		switch v := value.(type) {
		case map[string]any:
			if image, ok := v["image"].(string); ok && image != "" {
				images[image] = true
			}
			for _, child := range v {
				walk(child)
			}
		case []any:
			for _, child := range v {
				walk(child)
			}
		}
	}
	walk(obj)
	out := make([]string, 0, len(images))
	for image := range images {
		out = append(out, image)
	}
	sort.Strings(out)
	return out
}

func trim(value string, limit int) string {
	value = strings.TrimSpace(value)
	if len(value) <= limit {
		return value
	}
	return value[:limit] + "..."
}

func metaNamespaceName(obj map[string]any) string {
	ns := mapString(obj, "metadata", "namespace")
	name := mapString(obj, "metadata", "name")
	if ns == "" {
		return name
	}
	return ns + "/" + name
}

func hasEndpointSubsets(obj map[string]any) bool {
	subsets, ok := obj["subsets"].([]any)
	return ok && len(subsets) > 0
}

func hasHighLint(results []analyzer.LintResult) bool {
	for _, result := range results {
		if result.Severity == "high" || result.Severity == "error" {
			return true
		}
	}
	return false
}

func mapString(obj map[string]any, keys ...string) string {
	var cur any = obj
	for _, key := range keys {
		m, ok := cur.(map[string]any)
		if !ok {
			return ""
		}
		cur = m[key]
	}
	if cur == nil {
		return ""
	}
	return fmt.Sprint(cur)
}

func nestedMap(obj map[string]any, keys ...string) (map[string]any, bool) {
	var cur any = obj
	for _, key := range keys {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		cur = m[key]
	}
	m, ok := cur.(map[string]any)
	return m, ok
}

func ownerRefs(obj map[string]any) []string {
	refs, _ := nestedSlice(obj, "metadata", "ownerReferences")
	out := []string{}
	for _, ref := range refs {
		m, ok := ref.(map[string]any)
		if !ok {
			continue
		}
		out = append(out, fmt.Sprint(m["kind"])+"/"+fmt.Sprint(m["name"]))
	}
	return out
}

func nestedSlice(obj map[string]any, keys ...string) ([]any, bool) {
	var cur any = obj
	for _, key := range keys {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		cur = m[key]
	}
	s, ok := cur.([]any)
	return s, ok
}

func SleepOrDone(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
