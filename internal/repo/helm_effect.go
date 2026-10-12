package repo

import (
	"context"
	"errors"
	"fmt"
	"github.com/fixora/kubectl-fixora/internal/analyzer"
	"github.com/fixora/kubectl-fixora/internal/fix"
	"github.com/fixora/kubectl-fixora/internal/kube"
	"github.com/fixora/kubectl-fixora/internal/redact"
	"github.com/fixora/kubectl-fixora/internal/repairpolicy"
	"github.com/fixora/kubectl-fixora/internal/shadow"
	"gopkg.in/yaml.v3"
	"reflect"
	"sort"
	"strings"
)

type HelmValidation struct {
	Status, Summary string
	Admitted        map[string]any `json:"-" yaml:"-"`
}
type HelmSourceDeps interface {
	Renderer() HelmRenderer
	ReadTarget(context.Context, ResourceKey) (map[string]any, error)
	Scope(context.Context, ResourceKey) (bool, error)
	ServerValidate(context.Context, string) (HelmValidation, error)
	ShadowVerify(context.Context, fix.Plan, string) (*repairpolicy.Receipt, error)
}
type HelmSourceRequest struct {
	Context       HelmRenderContext
	Finding       analyzer.Finding
	Plan          fix.Plan
	Mappings      []HelmMapping
	VerifyRuntime bool `json:"-" yaml:"-"`
}
type HelmSourcePlan struct {
	Status, Path, Preview                 string
	ChangedKeys, Warnings                 []string
	ServerValidation, RuntimeVerification string
	Proof                                 *HelmSourceProof `json:"-" yaml:"-"`
}
type HelmSourceProof struct {
	inputs                              *HelmInputs
	request                             HelmSourceRequest
	candidate                           HelmValuesCandidate
	key                                 ResourceKey
	liveBinding, effectBinding, binding string
	server, runtime                     string
	runtimeProof                        *repairpolicy.Proof
	receipt                             *repairpolicy.Receipt
	preview                             string
	keys                                []string
}

func PrepareHelmSource(ctx context.Context, req HelmSourceRequest, d HelmSourceDeps) (HelmSourcePlan, error) {
	out := HelmSourcePlan{Status: "mapping-review-required", ServerValidation: "pending", RuntimeVerification: "not-requested"}
	if d == nil || d.Renderer() == nil {
		return out, errors.New("Helm source validation dependencies are required")
	}
	if err := ctx.Err(); err != nil {
		return out, err
	}
	if req.Finding.GitOps.HelmRelease != "" && req.Context.Release != req.Finding.GitOps.HelmRelease {
		return out, errors.New("Helm release conflicts with diagnosis")
	}
	if req.Context.Namespace != req.Finding.Namespace || req.Plan.Namespace != req.Finding.Namespace || req.Plan.Resource != req.Finding.ResourceKind+"/"+req.Finding.ResourceName {
		return out, errors.New("source target differs from diagnosis")
	}
	in, err := CaptureHelmInputs(req.Context)
	if err != nil {
		return out, err
	}
	req.Context = in.context
	key := ResourceKey{kube.WorkloadVersion(req.Finding.ResourceKind), req.Finding.ResourceKind, req.Finding.Namespace, req.Finding.ResourceName}
	scope := func(k ResourceKey) (bool, error) { return d.Scope(ctx, k) }
	first, err := stableHelmRender(ctx, d.Renderer(), in, nil, scope)
	if err != nil {
		return out, err
	}
	base, err := first.Target(key)
	if err != nil {
		return out, err
	}
	expected, err := expectedHelmTarget(base, req.Plan.PatchYAML())
	if err != nil {
		return out, err
	}
	if helmHash(semanticHelm(base, "")) == helmHash(semanticHelm(expected, "")) {
		out.Status = "source-already-correct"
		return out, nil
	}
	allowed := map[string]bool{"image": true, "fix-architecture": true, "resources": true, "env": true, "probe": true}
	if !allowed[req.Plan.Strategy] {
		return out, errors.New("repair family requires source review")
	}
	if err = shadow.ValidateCandidatePatch(req.Plan.PatchYAML(), req.Plan.Strategy, req.Plan.Resource, req.Plan.Namespace); err != nil {
		return out, errors.New("workload candidate failed existing field policy")
	}
	if err = checkHelmContainerTargets(base, req.Plan.PatchYAML()); err != nil {
		return out, err
	}
	live, err := d.ReadTarget(ctx, key)
	if err != nil {
		return out, errors.New("live source target cannot be verified")
	}
	binding, err := helmLiveBinding(live, key, req)
	if err != nil {
		return out, err
	}
	if !reflect.DeepEqual(semanticHelm(normalizedHelmPodSpec(base), ""), semanticHelm(normalizedHelmPodSpec(live), "")) {
		return out, errors.New("rendered baseline does not match live workload configuration")
	}
	candidates, err := ProposeHelmValues(in, first, key, req.Plan.PatchYAML(), req.Mappings)
	if err != nil {
		return out, err
	}
	passed := []HelmValuesCandidate{}
	effects := []string{}
	for _, c := range candidates {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		after, err := stableHelmRender(ctx, d.Renderer(), in, c.edited, scope)
		if err != nil {
			continue
		}
		if !exactHelmEffect(first, after, key, expected) {
			continue
		}
		if err = d.Renderer().Lint(ctx, req.Context, in, c.edited); err != nil {
			continue
		}
		redundant := false
		for i := range c.edits {
			if len(c.edits) < 2 {
				break
			}
			reduced := append([]HelmValuesEdit{}, c.edits[:i]...)
			reduced = append(reduced, c.edits[i+1:]...)
			other, e := makeHelmCandidate(in, reduced)
			if e != nil {
				continue
			}
			less, e := stableHelmRender(ctx, d.Renderer(), in, other.edited, scope)
			if e == nil && exactHelmEffect(first, less, key, expected) {
				redundant = true
				break
			}
		}
		if redundant {
			continue
		}
		passed = append(passed, c)
		effects = append(effects, after.Digest())
	}
	if len(passed) == 0 {
		out.Status = "render-effect-mismatch"
		return out, errors.New("no values candidate renders exactly the reviewed change")
	}
	if len(passed) != 1 {
		return out, errors.New("multiple values edits render the repair; select explicit --helm-map paths")
	}
	candidate := passed[0]
	raw, _ := yaml.Marshal(expected)
	validation, err := d.ServerValidate(ctx, string(raw))
	if err != nil {
		out.Status = "server-validation-failed"
		out.ServerValidation = "failed"
		return out, errors.New("server validation failed; source was not written")
	}
	switch validation.Status {
	case "passed":
		if !sameHelmTargetIdentity(validation.Admitted, key) || !reflect.DeepEqual(semanticHelm(normalizedHelmPodSpec(expected), ""), semanticHelm(normalizedHelmPodSpec(validation.Admitted), "")) {
			return out, errors.New("server admission changed the reviewed workload")
		}
	case "pending":
	default:
		return out, errors.New("server validation did not explicitly pass or remain unavailable")
	}
	out.ServerValidation = validation.Status
	var receipt *repairpolicy.Receipt
	if req.VerifyRuntime {
		if !req.Plan.ApplyEligible || !req.Plan.CanApply || validation.Status != "passed" {
			return out, errors.New("runtime verification requires eligible repair and passed admission validation")
		}
		proof := req.Plan.PolicyProof
		if proof == nil {
			proof, err = repairpolicy.BindCommon(repairpolicy.Snapshot{Target: repairpolicy.Target{Kind: key.Kind, Name: key.Name, Namespace: key.Namespace, UID: mapString(live, "metadata", "uid")}, Object: live}, req.Plan.PatchYAML(), req.Plan.Strategy, shadow.ValidateCandidatePatch)
			if err != nil {
				return out, err
			}
			req.Plan.PolicyProof = proof
		}
		receipt, err = d.ShadowVerify(ctx, req.Plan, req.Plan.PatchYAML())
		if err != nil {
			return out, errors.New("rendered candidate shadow verification failed")
		}
		if err = receipt.Check(proof, req.Plan.PatchYAML()); err != nil {
			return out, err
		}
		out.RuntimeVerification = "shadow-verified"
	}
	keys := []string{}
	lines := []string{}
	for _, e := range candidate.edits {
		keys = append(keys, e.Pointer)
		value, _ := yaml.Marshal(e.Value)
		lines = append(lines, fmt.Sprintf("%s: %s", e.Pointer, redact.Text(strings.TrimSpace(string(value)))))
	}
	sort.Strings(keys)
	sort.Strings(lines)
	out.Status = "source-validated"
	out.Path = in.context.EditValuesPath
	out.ChangedKeys = keys
	out.Preview = strings.Join(lines, "\n") + "\n"
	p := &HelmSourceProof{inputs: in, request: req, candidate: candidate, key: key, liveBinding: binding, effectBinding: helmHash([]string{first.Digest(), effects[0]}), server: out.ServerValidation, runtime: out.RuntimeVerification, runtimeProof: req.Plan.PolicyProof, receipt: receipt, preview: out.Preview, keys: append([]string(nil), keys...)}
	p.binding = p.contentBinding()
	out.Proof = p
	if err = p.CheckFresh(ctx, d); err != nil {
		return HelmSourcePlan{}, err
	}
	return out, nil
}
func stableHelmRender(ctx context.Context, r HelmRenderer, in *HelmInputs, edited []byte, scope HelmScopeFunc) (HelmInventory, error) {
	a, err := r.Render(ctx, in.context, in, edited)
	if err != nil {
		return HelmInventory{}, err
	}
	first, err := ParseHelmInventory(a, in.context.Namespace, scope)
	if err != nil {
		return first, err
	}
	b, err := r.Render(ctx, in.context, in, edited)
	if err != nil {
		return first, err
	}
	second, err := ParseHelmInventory(b, in.context.Namespace, scope)
	if err != nil || first.Digest() != second.Digest() {
		return HelmInventory{}, errors.New("Helm output is unstable or invalid")
	}
	return first, nil
}
func exactHelmEffect(before, after HelmInventory, key ResourceKey, expected map[string]any) bool {
	if len(before.objects) != len(after.objects) {
		return false
	}
	for k, obj := range before.objects {
		want := obj
		if k == key {
			want = expected
		}
		got, exists := after.objects[k]
		if !exists || helmHash(semanticHelm(want, "")) != helmHash(semanticHelm(got, "")) {
			return false
		}
	}
	return true
}
func (p HelmSourceProof) contentBinding() string {
	if p.inputs == nil {
		return ""
	}
	return helmHash([]any{p.inputs.fingerprint, p.inputs.rendererHash, p.request.Context, p.request.Plan.PatchYAML(), p.request.Plan.Strategy, p.request.Plan.ApplyEligible, p.request.Plan.CanApply, p.candidate.edited, p.candidate.edits, p.key, p.liveBinding, p.effectBinding, p.server, p.runtime, p.preview, p.keys})
}
func (p HelmSourceProof) CheckFresh(ctx context.Context, d HelmSourceDeps) error {
	if p.binding == "" || p.binding != p.contentBinding() || d == nil {
		return errors.New("fresh Helm source proof is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := p.inputs.CheckFresh(); err != nil {
		return err
	}
	live, err := d.ReadTarget(ctx, p.key)
	if err != nil {
		return errors.New("live source target is unreadable")
	}
	binding, err := helmLiveBinding(live, p.key, p.request)
	if err != nil || binding != p.liveBinding {
		return errors.New("live source target changed; prepare again")
	}
	if p.runtime == "shadow-verified" {
		return p.receipt.Check(p.runtimeProof, p.request.Plan.PatchYAML())
	}
	return nil
}
func helmLiveBinding(live map[string]any, key ResourceKey, req HelmSourceRequest) (string, error) {
	meta, _ := live["metadata"].(map[string]any)
	uid := stringField(meta, "uid")
	if live["kind"] != key.Kind || live["apiVersion"] != key.APIVersion || meta["name"] != key.Name || meta["namespace"] != key.Namespace || uid == "" || req.Finding.ResourceUID != "" && uid != req.Finding.ResourceUID || req.Plan.TargetUID != "" && uid != req.Plan.TargetUID {
		return "", errors.New("live source target identity changed")
	}
	annotations, _ := meta["annotations"].(map[string]any)
	if release := stringField(annotations, "meta.helm.sh/release-name"); release != "" && release != req.Context.Release {
		return "", errors.New("live release differs from render context")
	}
	if ns := stringField(annotations, "meta.helm.sh/release-namespace"); ns != "" && ns != req.Context.Namespace {
		return "", errors.New("live release namespace differs")
	}
	return helmHash([]any{key, uid, helmPodSpec(live), meta["labels"], meta["annotations"]}), nil
}
func mapString(m map[string]any, parent, key string) string {
	nested, _ := m[parent].(map[string]any)
	return stringField(nested, key)
}
func normalizedHelmPodSpec(obj map[string]any) map[string]any {
	spec := copyHelmObject(helmPodSpec(obj))
	for k, v := range map[string]any{"restartPolicy": "Always", "dnsPolicy": "ClusterFirst", "schedulerName": "default-scheduler", "terminationGracePeriodSeconds": float64(30), "enableServiceLinks": true, "serviceAccountName": "default", "serviceAccount": "default"} {
		if spec[k] == v {
			delete(spec, k)
		}
	}
	for _, section := range []string{"containers", "initContainers"} {
		cs, _ := spec[section].([]any)
		for _, raw := range cs {
			c, _ := raw.(map[string]any)
			for k, v := range map[string]any{"terminationMessagePath": "/dev/termination-log", "terminationMessagePolicy": "File", "imagePullPolicy": "IfNotPresent"} {
				if c[k] == v {
					delete(c, k)
				}
			}
			if m, ok := c["resources"].(map[string]any); ok && len(m) == 0 {
				delete(c, "resources")
			}
			for _, k := range []string{"readinessProbe", "livenessProbe", "startupProbe"} {
				probe, _ := c[k].(map[string]any)
				for f, v := range map[string]any{"timeoutSeconds": float64(1), "periodSeconds": float64(10), "successThreshold": float64(1), "failureThreshold": float64(3), "initialDelaySeconds": float64(0)} {
					if probe[f] == v {
						delete(probe, f)
					}
				}
				get, _ := probe["httpGet"].(map[string]any)
				if get["scheme"] == "HTTP" {
					delete(get, "scheme")
				}
			}
		}
	}
	return spec
}

func checkHelmContainerTargets(base map[string]any, patch string) error {
	var obj map[string]any
	if yaml.Unmarshal([]byte(patch), &obj) != nil {
		return errors.New("invalid workload candidate")
	}
	before, delta := helmPodSpec(base), helmPodSpec(obj)
	for _, section := range []string{"containers", "initContainers"} {
		known := map[string]bool{}
		list, _ := before[section].([]any)
		for _, raw := range list {
			c, _ := raw.(map[string]any)
			name := stringField(c, "name")
			if name == "" || known[name] {
				return errors.New("source container identity is ambiguous")
			}
			known[name] = true
		}
		changes, _ := delta[section].([]any)
		for _, raw := range changes {
			c, _ := raw.(map[string]any)
			if !known[stringField(c, "name")] {
				return errors.New("candidate container identity differs from source")
			}
		}
	}
	return nil
}

func sameHelmTargetIdentity(obj map[string]any, key ResourceKey) bool {
	meta, _ := obj["metadata"].(map[string]any)
	return obj["apiVersion"] == key.APIVersion && obj["kind"] == key.Kind && meta["name"] == key.Name && meta["namespace"] == key.Namespace
}
