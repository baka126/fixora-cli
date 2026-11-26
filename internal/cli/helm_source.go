package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"gopkg.in/yaml.v3"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/fixora/kubectl-fixora/internal/analyzer"
	"github.com/fixora/kubectl-fixora/internal/fix"
	"github.com/fixora/kubectl-fixora/internal/kube"
	"github.com/fixora/kubectl-fixora/internal/output"
	"github.com/fixora/kubectl-fixora/internal/repairpolicy"
	"github.com/fixora/kubectl-fixora/internal/repo"
	"github.com/fixora/kubectl-fixora/internal/shadow"
)

func hasHelmSourceContext(o options) bool {
	return o.helmChart != "" || o.helmRelease != "" || o.helmEditValues != "" || o.helmRenderMode != "" || o.helmKubeVersion != "" || len(o.helmValues)+len(o.helmAPIVersions)+len(o.helmMappings) > 0
}
func helmSourceContext(o options, ns string) (repo.HelmRenderContext, []repo.HelmMapping, error) {
	c := repo.HelmRenderContext{RepoPath: o.repoPath, ChartPath: o.helmChart, Release: o.helmRelease, Namespace: ns, EditValuesPath: o.helmEditValues, ValuesFiles: append([]string(nil), o.helmValues...), RenderMode: o.helmRenderMode, KubeVersion: o.helmKubeVersion, APIVersions: append([]string(nil), o.helmAPIVersions...)}
	mappings := []repo.HelmMapping{}
	for _, entry := range o.helmMappings {
		parts := strings.Split(entry, "=")
		if len(parts) != 2 || !strings.HasPrefix(parts[0], "/") || !strings.HasPrefix(parts[1], "/") {
			return c, nil, errors.New("--helm-map requires rendered JSON pointer=values JSON pointer")
		}
		mappings = append(mappings, repo.HelmMapping{FieldPointer: parts[0], ValuesPointer: parts[1]})
	}
	if c.RepoPath == "" || c.ChartPath == "" || c.Release == "" || c.EditValuesPath == "" || (c.RenderMode != "install" && c.RenderMode != "upgrade") {
		return c, nil, errors.New("Helm source delivery requires --repo, --helm-chart, --helm-release, --helm-edit-values and --helm-render-mode install|upgrade")
	}
	return c, mappings, nil
}

type helmCLIDeps struct {
	reader  kube.Reader
	k       kube.Kubectl
	opts    options
	finding analyzer.Finding
}

func (d helmCLIDeps) Renderer() repo.HelmRenderer { return repo.NewHelmRenderer() }
func (d helmCLIDeps) ReadTarget(ctx context.Context, key repo.ResourceKey) (map[string]any, error) {
	return d.reader.GetResource(ctx, key.Namespace, key.Kind+"/"+key.Name)
}
func (d helmCLIDeps) Scope(ctx context.Context, key repo.ResourceKey) (bool, error) {
	path := "/apis/" + key.APIVersion
	if !strings.Contains(key.APIVersion, "/") {
		path = "/api/" + key.APIVersion
	}
	raw, err := d.reader.Run(ctx, "get", "--raw", path)
	if err != nil {
		return false, errors.New("API resource scope discovery unavailable")
	}
	var list struct {
		Resources []struct {
			Kind, Name string
			Namespaced bool
		} `json:"resources"`
	}
	if json.Unmarshal(raw, &list) != nil {
		return false, errors.New("invalid resource discovery")
	}
	for _, r := range list.Resources {
		if r.Kind == key.Kind && !strings.Contains(r.Name, "/") {
			return r.Namespaced, nil
		}
	}
	return false, errors.New("rendered kind scope is unknown; source review required")
}
func (d helmCLIDeps) ServerValidate(ctx context.Context, patch string) (repo.HelmValidation, error) {
	var obj map[string]any
	if json.Unmarshal([]byte(patch), &obj) != nil { // Reviewed candidate arrives as YAML.
		var err error
		obj, err = decodeHelmAdmissionYAML(patch)
		if err != nil {
			return repo.HelmValidation{}, err
		}
	}
	meta, ok := obj["metadata"].(map[string]any)
	if !ok {
		return repo.HelmValidation{}, errors.New("target metadata required")
	}
	ns, _ := meta["namespace"].(string)
	name, _ := meta["name"].(string)
	kind, _ := obj["kind"].(string)
	live, err := d.reader.GetResource(ctx, ns, kind+"/"+name)
	if err != nil {
		return repo.HelmValidation{}, errors.New("admission target unreadable")
	}
	lm, _ := live["metadata"].(map[string]any)
	uid, _ := lm["uid"].(string)
	rv, _ := lm["resourceVersion"].(string)
	if uid == "" || rv == "" {
		return repo.HelmValidation{}, errors.New("admission requires target UID and resource version")
	}
	meta["uid"], meta["resourceVersion"] = uid, rv
	raw, err := json.Marshal(obj)
	if err != nil {
		return repo.HelmValidation{}, err
	}
	f, err := os.CreateTemp("", "fixora-helm-admission-*.json")
	if err != nil {
		return repo.HelmValidation{}, err
	}
	defer os.Remove(f.Name())
	_, err = f.Write(raw)
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return repo.HelmValidation{}, err
	}
	admitted, err := d.k.Run(ctx, "apply", "--server-side", "--force-conflicts", "--field-manager=fixora-source-validation", "--dry-run=server", "-f", f.Name(), "-o", "json")
	if err != nil {
		return repo.HelmValidation{}, errors.New("server dry-run rejected or unavailable; source remains unchanged")
	}
	var result map[string]any
	if json.Unmarshal(admitted, &result) != nil {
		return repo.HelmValidation{}, errors.New("invalid admitted target")
	}
	rm, _ := result["metadata"].(map[string]any)
	if rm["uid"] != uid {
		return repo.HelmValidation{}, errors.New("admitted target identity changed")
	}
	return repo.HelmValidation{Status: "passed", Admitted: result}, nil
}
func (d helmCLIDeps) ShadowVerify(ctx context.Context, p fix.Plan, patch string) (*repairpolicy.Receipt, error) {
	typed, err := kube.NewRequiredTypedClient(d.opts.context, "Helm candidate shadow verification")
	if err != nil {
		return nil, err
	}
	result, err := shadow.Run(ctx, typed, shadow.Request{Namespace: p.Namespace, Resource: p.Resource, Patch: patch, Finding: d.finding, Plan: p, Timeout: d.opts.shadowTimeout, Egress: d.opts.shadowEgress, Delivery: shadow.DeliveryPatch, Redact: true})
	if err != nil || !result.Verified || result.PolicyReceipt == nil {
		return nil, errors.New("Helm source candidate did not pass shadow verification")
	}
	return result.PolicyReceipt, nil
}
func helmCapabilities(ctx context.Context, r kube.Reader, c *repo.HelmRenderContext) error {
	if c.KubeVersion == "" {
		raw, err := r.Run(ctx, "get", "--raw", "/version")
		if err != nil {
			return errors.New("specify --helm-kube-version when server capabilities are unavailable")
		}
		var v struct {
			GitVersion string `json:"gitVersion"`
		}
		if json.Unmarshal(raw, &v) != nil || v.GitVersion == "" {
			return errors.New("server version unavailable")
		}
		c.KubeVersion = v.GitVersion
	}
	if len(c.APIVersions) == 0 {
		raw, err := r.Run(ctx, "api-versions")
		if err != nil {
			return errors.New("specify --helm-api-version when server capabilities are unavailable")
		}
		c.APIVersions = strings.Fields(string(raw))
		versions := append([]string(nil), c.APIVersions...)
		for _, version := range versions {
			path := "/apis/" + version
			if !strings.Contains(version, "/") {
				path = "/api/" + version
			}
			raw, err := r.Run(ctx, "get", "--raw", path)
			if err != nil {
				return errors.New("full API discovery unavailable; supply explicit --helm-api-version render capabilities")
			}
			var list struct {
				Resources []struct{ Kind, Name string } `json:"resources"`
			}
			if json.Unmarshal(raw, &list) != nil {
				return errors.New("invalid API capability discovery")
			}
			for _, resource := range list.Resources {
				if resource.Kind != "" && !strings.Contains(resource.Name, "/") {
					c.APIVersions = appendUniqueString(c.APIVersions, version+"/"+resource.Kind)
				}
			}
		}
		if len(c.APIVersions) == 0 {
			return errors.New("render API capabilities unavailable")
		}
	}
	return nil
}
func prepareHelmSource(ctx context.Context, r kube.Reader, k kube.Kubectl, o options, f analyzer.Finding, p fix.Plan) (repo.HelmSourcePlan, error) {
	c, m, err := helmSourceContext(o, f.Namespace)
	if err != nil {
		return repo.HelmSourcePlan{}, err
	}
	if err = helmCapabilities(ctx, r, &c); err != nil {
		return repo.HelmSourcePlan{}, err
	}
	return repo.PrepareHelmSource(ctx, repo.HelmSourceRequest{Context: c, Finding: f, Plan: p, Mappings: m, VerifyRuntime: false}, helmCLIDeps{r, k, o, f})
}
func runHelmSourceDelivery(ctx context.Context, stdout, stderr io.Writer, o options, r kube.Reader, k kube.Kubectl, f analyzer.Finding, p fix.Plan) int {
	if !p.ApplyEligible && (f.Status == "Healthy" || f.Status == "OwnedPodsHealthy") {
		if o.output != "text" {
			return output.Write(stdout, o.output, p)
		}
		fmt.Fprintf(stdout, "%s (%s): healthy. No repair needed; no changes made.\n", p.Resource, f.Namespace)
		return 0
	}
	if o.apply || strings.EqualFold(o.delivery, "cluster") {
		fmt.Fprintln(stderr, "error: Helm source context requires source delivery; direct cluster delivery is blocked")
		return 2
	}
	if o.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, o.timeout)
		defer cancel()
	}
	c, m, err := helmSourceContext(o, f.Namespace)
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return 2
	}
	if err = helmCapabilities(ctx, r, &c); err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return 2
	}
	deps := helmCLIDeps{r, k, o, f}
	req := repo.HelmSourceRequest{Context: c, Finding: f, Plan: p, Mappings: m}
	return deliverHelmSource(ctx, stdout, stderr, o, req, deps)
}
func deliverHelmSource(ctx context.Context, stdout, stderr io.Writer, o options, req repo.HelmSourceRequest, deps repo.HelmSourceDeps) int {
	root, err := filepath.Abs(req.Context.RepoPath)
	if err == nil {
		root, err = filepath.EvalSymlinks(root)
	}
	if err != nil {
		fmt.Fprintln(stderr, "error: source repository unavailable")
		return 1
	}
	req.Context.RepoPath = root
	plan, err := repo.PrepareHelmSource(ctx, req, deps)
	if err != nil {
		fmt.Fprintln(stderr, "error: Helm source validation:", err)
		return 1
	}
	if o.output != "text" {
		return output.Write(stdout, o.output, plan)
	}
	fmt.Fprintf(stdout, "%s (%s): %s\n", req.Plan.Resource, req.Plan.Namespace, plan.Status)
	if plan.Proof == nil {
		return 0
	}
	fmt.Fprintf(stdout, "Values: %s\n%sAdmission: %s; runtime: %s; reconciliation: pending\n", plan.Path, plan.Preview, plan.ServerValidation, plan.RuntimeVerification)
	if o.preview || (!o.sourcePatch && !canonicalPRDelivery(o) && !o.commit) {
		return 0
	}
	if !o.yes {
		fmt.Fprintln(stderr, "error: use --yes after reviewing this values preview to confirm source write or PR delivery")
		return 2
	}
	original := plan
	if req.Plan.ApplyEligible && req.Plan.CanApply {
		req.VerifyRuntime = true
		plan, err = repo.PrepareHelmSource(ctx, req, deps)
		if err != nil {
			fmt.Fprintln(stderr, "error: Helm runtime verification:", err)
			return 1
		}
	}
	if err = original.Proof.CheckFresh(ctx, deps); err != nil {
		fmt.Fprintln(stderr, "error: proposal changed after preview:", err)
		return 1
	}
	written, err := repo.WriteValidatedHelmSource(ctx, plan, deps, fix.SourceReviewAllowed(req.Plan))
	if err != nil {
		if written.SourceStatus == "source-written" {
			fmt.Fprintf(stderr, "Source was written to %s; subsequent validation failed. No commit or PR was attempted; reconciliation remains pending.\n", written.Path)
		}
		fmt.Fprintln(stderr, "error:", err)
		return 1
	}
	fmt.Fprintf(stdout, "Source: %s; admission: %s; runtime: %s; reconciliation: %s\n", written.Path, written.ServerValidation, written.RuntimeVerification, written.Reconciliation)
	if !canonicalPRDelivery(o) && !o.commit {
		return 0
	}
	if err = plan.Proof.CheckWritten(ctx, deps); err != nil {
		fmt.Fprintln(stderr, "error: precommit validation:", err)
		return 1
	}
	branch := o.branch
	if branch == "" {
		branch = "fixora/helm-" + strings.ToLower(req.Finding.ResourceName)
	}
	title := "fixora: validated Helm source proposal for " + req.Plan.Resource
	if err = repo.PrepareBranchFiles(ctx, req.Context.RepoPath, branch, true, title, []string{written.Path}); err != nil {
		fmt.Fprintln(stderr, "error: source written, commit failed:", err)
		return 1
	}
	if !canonicalPRDelivery(o) {
		fmt.Fprintf(stdout, "Committed source on %s; reconciliation pending.\n", branch)
		return 0
	}
	body := fmt.Sprintf("Propose a certified values change for %s in %s.\n\nEdited keys: %s\nAdmission: %s\nRuntime: %s\nProduction reconciliation: pending.\n", req.Plan.Resource, req.Plan.Namespace, strings.Join(plan.ChangedKeys, ", "), plan.ServerValidation, plan.RuntimeVerification)
	pr, err := repo.OpenPullRequest(ctx, req.Context.RepoPath, branch, o.prBase, firstArg([]string{o.prTitle}, title), body, true)
	if err != nil {
		fmt.Fprintln(stderr, "error: source committed; PR delivery incomplete:", err)
		return 1
	}
	fmt.Fprintf(stdout, "Branch: %s; pushed: %t; PR opened: %t\n", pr.Branch, pr.Pushed, pr.Opened)
	if pr.URL != "" {
		fmt.Fprintln(stdout, pr.URL)
	}
	return 0
}

func decodeHelmAdmissionYAML(s string) (map[string]any, error) {
	var obj map[string]any
	err := yaml.Unmarshal([]byte(s), &obj)
	return obj, err
}
