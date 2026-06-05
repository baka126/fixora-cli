package analyzer

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"unicode"

	"github.com/fixora/kubectl-fixora/internal/kube"
	"github.com/fixora/kubectl-fixora/internal/redact"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

func New(k kube.Reader, opts Options) Analyzer {
	return Analyzer{k: k, opts: opts}
}

func (a Analyzer) ScanIncidents(ctx context.Context) ([]Finding, error) {
	report := a.ScanReport(ctx)
	if len(report.Findings) == 0 && len(report.Skipped) > 0 {
		return nil, fmt.Errorf("%s", report.Skipped[0].Reason)
	}
	return report.Findings, nil
}

func (a Analyzer) ScanReport(ctx context.Context) ScanReport {
	sctx := NewScanContext(ctx, a.k, a.opts)
	findings := []Finding{}
	skipped := []SkippedCheck{}
	selected := filterSet(a.opts.Filters)

	var wg sync.WaitGroup
	var mu sync.Mutex

	wg.Add(1)
	go func() {
		defer wg.Done()
		if len(selected) == 0 || selected["pod"] || selected["pods"] {
			pods, err := sctx.GetPods()
			if err != nil {
				mu.Lock()
				skipped = append(skipped, SkippedCheck{Name: "pods", Reason: err.Error()})
				mu.Unlock()
				return
			}
			events, err := sctx.GetEvents()
			if err != nil {
				mu.Lock()
				skipped = append(skipped, SkippedCheck{Name: "events", Reason: err.Error()})
				mu.Unlock()
				events = nil
			}

			eventIndex := make(map[string][]kube.Event)
			unindexedEvents := []kube.Event{}
			for _, e := range events {
				if e.InvolvedObject.Name != "" {
					key := firstNonEmpty(e.InvolvedObject.Namespace, e.Metadata.Namespace) + "/" + e.InvolvedObject.Name
					eventIndex[key] = append(eventIndex[key], e)
					continue
				}
				unindexedEvents = append(unindexedEvents, e)
			}

			workerCount := a.opts.MaxConcurrency
			if workerCount <= 0 {
				workerCount = 10
			}
			if workerCount > len(pods.Items) && len(pods.Items) > 0 {
				workerCount = len(pods.Items)
			}
			podCh := make(chan kube.Pod)
			var logWg sync.WaitGroup
			tracer := otel.Tracer("fixora/analyzer")
			for i := 0; i < workerCount; i++ {
				logWg.Add(1)
				go func() {
					defer logWg.Done()
					for p := range podCh {
						if ctx.Err() != nil {
							return
						}

						workerCtx, span := tracer.Start(ctx, "AnalyzePod", trace.WithAttributes(
							attribute.String("pod.namespace", p.Metadata.Namespace),
							attribute.String("pod.name", p.Metadata.Name),
						))

						key := p.Metadata.Namespace + "/" + p.Metadata.Name
						relatedEvents := append([]kube.Event{}, eventIndex[key]...)
						for _, event := range unindexedEvents {
							if event.Metadata.Namespace == p.Metadata.Namespace && strings.Contains(event.Message, p.Metadata.Name) {
								relatedEvents = append(relatedEvents, event)
							}
						}
						finding, ok := a.findingForPod(workerCtx, p, relatedEvents)
						if ok {
							mu.Lock()
							findings = append(findings, finding)
							mu.Unlock()
						}
						span.End()
					}
				}()
			}
		sendPods:
			for _, p := range pods.Items {
				select {
				case <-ctx.Done():
					break sendPods
				case podCh <- p:
				}
			}
			close(podCh)
			logWg.Wait()
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		registryFindings, registrySkipped := a.runRegistry(sctx)
		mu.Lock()
		findings = append(findings, registryFindings...)
		skipped = append(skipped, registrySkipped...)
		mu.Unlock()
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		precisionFindings, precisionSkipped := a.runPrecisionAnalyzers(sctx)
		mu.Lock()
		findings = append(findings, precisionFindings...)
		skipped = append(skipped, precisionSkipped...)
		mu.Unlock()
	}()

	wg.Wait()

	findings = dedupe(findings)
	sort.Slice(findings, func(i, j int) bool {
		if findings[i].Severity != findings[j].Severity {
			return severityRank(findings[i].Severity) > severityRank(findings[j].Severity)
		}
		return findings[i].Namespace+"/"+findings[i].PodName < findings[j].Namespace+"/"+findings[j].PodName
	})
	return ScanReport{Findings: findings, Skipped: skipped, Summary: summarizeScan(findings, skipped)}
}

func (r ScanReport) Envelope() ScanEnvelope {
	status := "OK"
	if len(r.Findings) > 0 {
		status = "ProblemDetected"
	}
	warnings := []string{}
	for _, skipped := range r.Skipped {
		warnings = append(warnings, skipped.Name+": "+skipped.Reason)
	}
	return ScanEnvelope{
		APIVersion: "fixora.dev/v1alpha1",
		Kind:       "AnalysisReport",
		Status:     status,
		Provider:   firstNonEmpty(os.Getenv("FIXORA_AI_PROVIDER"), "local"),
		Problems:   len(r.Findings),
		Results:    r.Findings,
		Skipped:    r.Skipped,
		Warnings:   warnings,
		Summary:    r.Summary,
	}
}

func (a Analyzer) AnalyzeResource(ctx context.Context, resource string) (Finding, error) {
	kind, name := splitResource(resource)
	if kind == "" || name == "" {
		return Finding{}, fmt.Errorf("resource must look like kind/name")
	}
	ns := a.opts.Namespace
	if kind == "pod" || kind == "pods" {
		pod, err := a.k.GetPod(ctx, ns, name)
		if err != nil {
			return Finding{}, err
		}
		events, _ := a.k.GetEvents(ctx, ns, "")
		finding, ok := a.findingForPod(ctx, pod, events)
		if !ok {
			finding = a.healthyFinding(pod)
		}
		return finding, nil
	}
	obj, err := a.k.GetResource(ctx, ns, resource)
	if err != nil {
		return Finding{}, err
	}
	if isControllerKind(kind) {
		return a.findingForController(ctx, obj, kind, name, ns)
	}
	finding := a.findingForObject(obj, kind, name, ns)
	if a.opts.IncludeLogs {
		finding.Logs = append(finding.Logs, LogSnippet{Source: "logs", Text: "logs are collected for Pods; analyze the owning pod for container logs"})
	}
	return finding, nil
}

func (a Analyzer) findingForController(ctx context.Context, obj map[string]any, kind, name, namespace string) (Finding, error) {
	finding := a.findingForObject(obj, kind, name, namespace)
	finding.Summary = "Controller inspection completed with owned pod evidence."
	finding.Evidence = append(finding.Evidence, Evidence{Label: "Controller status", Value: finding.Status})
	pods, err := a.k.GetPods(ctx, namespace, false)
	if err != nil {
		finding.Status = "PodsUnreadable"
		finding.Severity = "high"
		finding.Category = "rbac"
		finding.Summary = "Controller exists, but owned pods could not be listed."
		finding.Evidence = append(finding.Evidence, Evidence{Label: "Pod list error", Value: err.Error()})
		return finding, nil
	}
	events, _ := a.k.GetEvents(ctx, namespace, "")
	selector := controllerSelector(obj, kind)
	var related []kube.Pod
	for _, pod := range pods.Items {
		if pod.Metadata.Namespace != namespace {
			continue
		}
		if podOwnedBy(pod, kind, name) || labelsMatch(selector, pod.Metadata.Labels) {
			related = append(related, pod)
		}
	}
	if len(related) == 0 {
		finding.Status = "NoOwnedPods"
		finding.Severity = "medium"
		finding.Summary = "Controller has no readable owned pods in scope."
		finding.Evidence = append(finding.Evidence, Evidence{Label: "Owned pods", Value: "0"})
		return finding, nil
	}
	finding.Evidence = append(finding.Evidence, Evidence{Label: "Owned pods", Value: fmt.Sprint(len(related))})
	var failures []Finding
	for _, pod := range related {
		podEvents := eventsForPod(events, pod)
		if pf, ok := a.findingForPod(ctx, pod, podEvents); ok {
			failures = append(failures, pf)
		}
	}
	if len(failures) == 0 {
		finding.Status = "OwnedPodsHealthy"
		finding.Severity = "info"
		finding.Summary = "Controller was inspected and no failing owned pod was detected."
		return finding, nil
	}
	sort.Slice(failures, func(i, j int) bool {
		return severityRank(failures[i].Severity) > severityRank(failures[j].Severity)
	})
	top := failures[0]
	top.ResourceKind = normalizeControllerKind(kind)
	top.ResourceName = name
	top.ID = namespace + "/" + top.ResourceKind + "/" + name + "/" + top.PodName + "/" + top.Status
	top.Summary = "Controller-owned pod is failing: " + top.Summary
	top.Evidence = append([]Evidence{{Label: "Controller status", Value: finding.Status}}, top.Evidence...)
	top.Evidence = append(top.Evidence, Evidence{Label: "Owned pod failures", Value: summarizeFailures(failures)})
	return top, nil
}

func isControllerKind(kind string) bool {
	switch strings.ToLower(kind) {
	case "deployment", "deploy", "deployments", "statefulset", "statefulsets", "sts", "daemonset", "daemonsets", "ds", "replicaset", "replicasets", "rs", "job", "jobs", "cronjob", "cronjobs", "cj":
		return true
	default:
		return false
	}
}

func normalizeControllerKind(kind string) string {
	switch strings.ToLower(kind) {
	case "deploy", "deployment", "deployments":
		return "Deployment"
	case "sts", "statefulset", "statefulsets":
		return "StatefulSet"
	case "ds", "daemonset", "daemonsets":
		return "DaemonSet"
	case "rs", "replicaset", "replicasets":
		return "ReplicaSet"
	case "job", "jobs":
		return "Job"
	case "cj", "cronjob", "cronjobs":
		return "CronJob"
	default:
		return toTitle(kind)
	}
}

func controllerSelector(obj map[string]any, kind string) map[string]string {
	spec := nestedMapAny(obj, "spec")
	if strings.EqualFold(kind, "cronjob") || strings.EqualFold(kind, "cronjobs") || strings.EqualFold(kind, "cj") {
		spec = nestedMapAny(obj, "spec", "jobTemplate", "spec")
	}
	selector := stringMap(nestedMapAny(spec, "selector", "matchLabels"))
	if len(selector) > 0 {
		return selector
	}
	return stringMap(nestedMapAny(spec, "template", "metadata", "labels"))
}

func podOwnedBy(pod kube.Pod, kind, name string) bool {
	wantKind := normalizeControllerKind(kind)
	for _, ref := range pod.Metadata.OwnerRefs {
		if strings.EqualFold(ref.Kind, wantKind) && ref.Name == name {
			return true
		}
		if wantKind == "Deployment" && strings.EqualFold(ref.Kind, "ReplicaSet") && strings.HasPrefix(ref.Name, name+"-") {
			return true
		}
		if wantKind == "CronJob" && strings.EqualFold(ref.Kind, "Job") && strings.HasPrefix(ref.Name, name+"-") {
			return true
		}
	}
	return false
}

func labelsMatch(selector, labels map[string]string) bool {
	if len(selector) == 0 {
		return false
	}
	for key, value := range selector {
		if labels[key] != value {
			return false
		}
	}
	return true
}

func eventsForPod(events []kube.Event, pod kube.Pod) []kube.Event {
	var out []kube.Event
	for _, event := range events {
		if event.InvolvedObject.Name == pod.Metadata.Name || (event.Metadata.Namespace == pod.Metadata.Namespace && strings.Contains(event.Message, pod.Metadata.Name)) {
			out = append(out, event)
		}
	}
	return out
}

func summarizeFailures(findings []Finding) string {
	counts := map[string]int{}
	for _, f := range findings {
		counts[f.Status]++
	}
	var parts []string
	for status, count := range counts {
		parts = append(parts, fmt.Sprintf("%s=%d", status, count))
	}
	sort.Strings(parts)
	return strings.Join(parts, ", ")
}

func (a Analyzer) Predict(ctx context.Context) ([]Prediction, error) {
	pods, err := a.k.GetPods(ctx, a.opts.Namespace, a.opts.AllNS)
	if err != nil {
		return nil, err
	}
	out := []Prediction{}
	for _, pod := range pods.Items {
		restarts := 0
		for _, status := range append(pod.Status.ContainerStatuses, pod.Status.InitStatuses...) {
			restarts += status.RestartCount
		}
		if restarts >= 3 {
			out = append(out, Prediction{
				Namespace:   pod.Metadata.Namespace,
				PodName:     pod.Metadata.Name,
				Signal:      "restart trend",
				Risk:        "medium",
				Confidence:  minInt(90, 40+restarts*5),
				Evidence:    fmt.Sprintf("%d restarts observed", restarts),
				Recommended: "Inspect recent logs and deployment changes before it becomes a CrashLoopBackOff incident.",
			})
		}
		if len(pod.Spec.Containers) > 0 {
			for _, c := range pod.Spec.Containers {
				if c.Resources.Requests["memory"] == "" && c.Resources.Limits["memory"] == "" {
					out = append(out, Prediction{
						Namespace:   pod.Metadata.Namespace,
						PodName:     pod.Metadata.Name,
						Signal:      "oom risk",
						Risk:        "low",
						Confidence:  45,
						Evidence:    "container has no memory request or limit",
						Recommended: "Add memory requests and limits after observing actual usage.",
					})
				}
			}
		}
	}
	return out, nil
}

func (a Analyzer) Cost(ctx context.Context, rest []string) ([]CostRow, error) {
	if len(rest) == 0 || rest[0] == "nodes" {
		nodes, err := a.k.GetNodes(ctx)
		if err != nil {
			return nil, err
		}
		rows := []CostRow{}
		for _, node := range nodes {
			vendor, region, instanceType := nodePricingMetadata(node)
			rows = append(rows, CostRow{
				Name:         node.Metadata.Name,
				Kind:         "Node",
				Region:       region,
				InstanceType: instanceType,
				MonthlyUSD:   estimateMonthlyUSD(vendor, region, instanceType),
				Note:         "approximate static catalog estimate",
			})
		}
		return rows, nil
	}
	pods, err := a.k.GetPods(ctx, a.opts.Namespace, a.opts.AllNS)
	if err != nil {
		return nil, err
	}
	rows := []CostRow{}
	for _, pod := range pods.Items {
		rows = append(rows, CostRow{
			Name: pod.Metadata.Namespace + "/" + pod.Metadata.Name,
			Kind: "Pod",
			Note: "workload cost needs resource requests and node pricing; use cost nodes for node estimate",
		})
	}
	return rows, nil
}

func (a Analyzer) findingForPod(ctx context.Context, pod kube.Pod, events []kube.Event) (Finding, bool) {
	status, category, severity := podProblem(pod)
	if status == "" {
		return Finding{}, false
	}
	kind, name := a.resolveTopOwner(ctx, pod.Metadata.Namespace, topOwnerKind(pod), topOwnerName(pod))
	f := Finding{
		ID:           pod.Metadata.Namespace + "/" + pod.Metadata.Name + "/" + status,
		Namespace:    pod.Metadata.Namespace,
		ResourceKind: kind,
		ResourceName: name,
		PodName:      pod.Metadata.Name,
		Status:       status,
		Severity:     severity,
		Category:     category,
		Summary:      summaryForStatus(status),
		OwnerChain:   ownerChain(pod),
		GitOps:       gitOpsHints(pod.Metadata.Labels, pod.Metadata.Annotations),
		Evidence: []Evidence{
			{Label: "Pod phase", Value: pod.Status.Phase},
			{Label: "Node", Value: pod.Spec.NodeName},
		},
		Recommendations: recommendationsForStatus(status, pod),
	}

	corr, recent := CorrelateRecentEvents(events)
	f.ChangeCorrelation = corr
	f.RecentChanges = recent

	for _, event := range events {
		if event.Metadata.Namespace == pod.Metadata.Namespace && strings.Contains(event.Message, pod.Metadata.Name) {
			f.Evidence = append(f.Evidence, Evidence{Label: "Event " + event.Reason, Value: event.Message})
		}
	}
	if a.opts.IncludeLogs {
		if logs, err := a.k.Logs(ctx, pod.Metadata.Namespace, pod.Metadata.Name, false); err == nil && logs != "" {
			f.Logs = append(f.Logs, LogSnippet{Source: "current", Text: AggregateLogs(a.redact(logs))})
		}
		if logs, err := a.k.Logs(ctx, pod.Metadata.Namespace, pod.Metadata.Name, true); err == nil && logs != "" {
			f.Logs = append(f.Logs, LogSnippet{Source: "previous", Text: AggregateLogs(a.redact(logs))})
		}
	}
	return f, true
}

func (a Analyzer) findingForObject(obj map[string]any, kind, name, namespace string) Finding {
	labels, annotations := objectLabelsAnnotations(obj)
	status := "Unknown"
	if s, ok := obj["status"].(map[string]any); ok {
		status = compactMap(s)
	}
	return Finding{
		ID:           namespace + "/" + kind + "/" + name,
		Namespace:    namespace,
		ResourceKind: toTitle(kind),
		ResourceName: name,
		Status:       status,
		Severity:     "info",
		Category:     "workload",
		Summary:      "Resource inspection completed. Analyze related pods for container-level failure evidence.",
		GitOps:       gitOpsHints(labels, annotations),
		Evidence:     []Evidence{{Label: "Status", Value: status}},
		Recommendations: []Recommendation{{
			Title:         "Inspect owned pods",
			Description:   "Controller resources often need pod, event, service, and endpoint evidence before a safe fix can be suggested.",
			SafeByDefault: true,
		}},
	}
}

func toTitle(s string) string {
	if s == "" {
		return ""
	}
	r := []rune(s)
	r[0] = unicode.ToUpper(r[0])
	return string(r)
}

func (a Analyzer) healthyFinding(pod kube.Pod) Finding {
	return Finding{
		ID:           pod.Metadata.Namespace + "/" + pod.Metadata.Name + "/healthy",
		Namespace:    pod.Metadata.Namespace,
		ResourceKind: topOwnerKind(pod),
		ResourceName: topOwnerName(pod),
		PodName:      pod.Metadata.Name,
		Status:       "Healthy",
		Severity:     "info",
		Category:     "runtime",
		Summary:      "No obvious pod failure was detected from status.",
		OwnerChain:   ownerChain(pod),
		GitOps:       gitOpsHints(pod.Metadata.Labels, pod.Metadata.Annotations),
	}
}

func (a Analyzer) eventNamespace() string {
	if a.opts.AllNS {
		return ""
	}
	return a.opts.Namespace
}

func (a Analyzer) redact(value string) string {
	if !a.opts.Redact {
		return value
	}
	return redact.KubernetesText(value)
}

func podProblem(pod kube.Pod) (status, category, severity string) {
	for _, cs := range append(pod.Status.InitStatuses, pod.Status.ContainerStatuses...) {
		for stateName, state := range cs.State {
			reason := firstNonEmpty(state.Reason, stateName)
			switch {
			case strings.Contains(reason, "CrashLoopBackOff"):
				return "CrashLoopBackOff", "runtime", "critical"
			case strings.Contains(reason, "ImagePullBackOff"), strings.Contains(reason, "ErrImagePull"):
				return reason, "image", "high"
			case strings.Contains(reason, "CreateContainerConfigError"):
				return reason, "configuration", "high"
			case strings.Contains(reason, "RunContainerError"), strings.Contains(reason, "CreateContainerError"):
				return reason, "runtime", "high"
			}
		}
		for _, state := range cs.LastState {
			if strings.Contains(state.Reason, "OOMKilled") {
				return "OOMKilled", "resources", "high"
			}
		}
	}
	if pod.Status.Phase == "Pending" {
		return "Pending", "scheduling", "medium"
	}
	if pod.Status.Phase == "Failed" || pod.Status.Reason != "" {
		return firstNonEmpty(pod.Status.Reason, "PodFailed"), "runtime", "high"
	}
	for _, condition := range pod.Status.Conditions {
		if condition.Status == "False" && condition.Reason != "" {
			if strings.Contains(condition.Reason, "Unschedulable") {
				return "Unschedulable", "scheduling", "high"
			}
		}
	}
	return "", "", ""
}

func recommendationsForStatus(status string, pod kube.Pod) []Recommendation {
	switch {
	case strings.Contains(status, "ImagePull"):
		return []Recommendation{{Title: "Verify image reference", Description: "Check repository, tag, imagePullSecrets, registry auth, platform compatibility, and avoid floating tags.", PatchType: "image", SafeByDefault: true}}
	case strings.Contains(status, "OOMKilled"):
		return []Recommendation{{Title: "Right-size memory", Description: "Compare usage against requests and limits before raising limits or reducing workload memory demand.", PatchType: "resources", SafeByDefault: false}}
	case strings.Contains(status, "CrashLoopBackOff"):
		return []Recommendation{{Title: "Inspect logs and probes", Description: "Review previous logs, command/args, env refs, config mounts, securityContext, and probe timing.", PatchType: "runtime", SafeByDefault: false}}
	case strings.Contains(status, "Config"):
		return []Recommendation{{Title: "Validate ConfigMap and Secret refs", Description: "Check env, envFrom, volumes, and required keys. Never print secret values.", PatchType: "env", SafeByDefault: true}}
	case strings.Contains(status, "Pending"), strings.Contains(status, "Unschedulable"):
		return []Recommendation{{Title: "Review scheduling constraints", Description: "Check nodeSelector, affinity, taints, tolerations, PVC binding, and resource requests.", PatchType: "scheduling", SafeByDefault: false}}
	default:
		return []Recommendation{{Title: "Collect related evidence", Description: "Inspect events, logs, owner chain, services, endpoints, and GitOps ownership before patching.", SafeByDefault: true}}
	}
}

func summaryForStatus(status string) string {
	switch {
	case strings.Contains(status, "ImagePull"):
		return "Container image could not be pulled."
	case strings.Contains(status, "OOMKilled"):
		return "Container was terminated after exceeding memory constraints."
	case strings.Contains(status, "CrashLoopBackOff"):
		return "Container is repeatedly crashing after start."
	case strings.Contains(status, "Pending"), strings.Contains(status, "Unschedulable"):
		return "Pod cannot be scheduled or started."
	default:
		return "Kubernetes reported a workload failure."
	}
}

func gitOpsHints(labels, annotations map[string]string) GitOpsHints {
	h := GitOpsHints{}
	if labels == nil {
		labels = map[string]string{}
	}
	if annotations == nil {
		annotations = map[string]string{}
	}
	h.ManagedBy = labels["app.kubernetes.io/managed-by"]
	h.HelmRelease = firstNonEmpty(annotations["meta.helm.sh/release-name"], labels["app.kubernetes.io/instance"])
	h.HelmChart = labels["helm.sh/chart"]
	if h.HelmRelease != "" || strings.EqualFold(h.ManagedBy, "Helm") {
		h.TargetAdvice = "Patch the Helm values source, not rendered Kubernetes YAML."
	}
	for key, value := range annotations {
		lower := strings.ToLower(key + "=" + value)
		if strings.Contains(lower, "argocd") {
			h.ArgoHint = key + "=" + value
		}
		if strings.Contains(lower, "fluxcd") || strings.Contains(lower, "kustomize.toolkit") || strings.Contains(lower, "helm.toolkit") {
			h.FluxHint = key + "=" + value
		}
	}
	return h
}

func ownerChain(pod kube.Pod) []string {
	chain := []string{"Pod/" + pod.Metadata.Name}
	for _, owner := range pod.Metadata.OwnerRefs {
		chain = append(chain, owner.Kind+"/"+owner.Name)
	}
	return chain
}

func (a Analyzer) resolveTopOwner(ctx context.Context, ns, kind, name string) (string, string) {
	if kind == "ReplicaSet" || kind == "Job" {
		obj, err := a.k.GetResource(ctx, ns, kind+"/"+name)
		if err == nil {
			meta, _ := obj["metadata"].(map[string]any)
			if owners, ok := meta["ownerReferences"].([]any); ok && len(owners) > 0 {
				if last, ok := owners[len(owners)-1].(map[string]any); ok {
					if parentKind, ok := last["kind"].(string); ok {
						if parentName, ok := last["name"].(string); ok {
							return a.resolveTopOwner(ctx, ns, parentKind, parentName)
						}
					}
				}
			}
		}
	}
	return kind, name
}

func topOwnerKind(pod kube.Pod) string {
	if len(pod.Metadata.OwnerRefs) == 0 {
		return "Pod"
	}
	return pod.Metadata.OwnerRefs[len(pod.Metadata.OwnerRefs)-1].Kind
}

func topOwnerName(pod kube.Pod) string {
	if len(pod.Metadata.OwnerRefs) == 0 {
		return pod.Metadata.Name
	}
	return pod.Metadata.OwnerRefs[len(pod.Metadata.OwnerRefs)-1].Name
}

func splitResource(resource string) (kind, name string) {
	parts := strings.SplitN(resource, "/", 2)
	if len(parts) != 2 {
		return "", ""
	}
	return strings.ToLower(parts[0]), parts[1]
}

func objectLabelsAnnotations(obj map[string]any) (map[string]string, map[string]string) {
	meta, _ := obj["metadata"].(map[string]any)
	return stringMap(meta["labels"]), stringMap(meta["annotations"])
}

func stringMap(value any) map[string]string {
	out := map[string]string{}
	m, ok := value.(map[string]any)
	if !ok {
		return out
	}
	for key, val := range m {
		out[key] = fmt.Sprint(val)
	}
	return out
}

func nestedMapAny(obj map[string]any, keys ...string) map[string]any {
	var cur any = obj
	for _, key := range keys {
		m, ok := cur.(map[string]any)
		if !ok {
			return map[string]any{}
		}
		cur = m[key]
	}
	m, ok := cur.(map[string]any)
	if !ok || m == nil {
		return map[string]any{}
	}
	return m
}

func compactMap(m map[string]any) string {
	parts := []string{}
	for key, value := range m {
		parts = append(parts, key+"="+fmt.Sprint(value))
	}
	sort.Strings(parts)
	out := strings.Join(parts, " ")
	if len(out) > 240 {
		return out[:240] + "..."
	}
	return out
}

func nodePricingMetadata(node kube.Node) (vendor, region, instanceType string) {
	labels := node.Metadata.Labels
	region = firstNonEmpty(labels["topology.kubernetes.io/region"], labels["failure-domain.beta.kubernetes.io/region"])
	instanceType = firstNonEmpty(labels["node.kubernetes.io/instance-type"], labels["beta.kubernetes.io/instance-type"])
	provider := node.Spec.ProviderID
	switch {
	case strings.HasPrefix(provider, "aws://"):
		vendor = "aws"
	case strings.HasPrefix(provider, "gce://"):
		vendor = "gcp"
	case strings.HasPrefix(provider, "azure://"):
		vendor = "azure"
	case strings.Contains(region, "amazonaws") || strings.HasPrefix(region, "us-") || strings.HasPrefix(region, "ap-") || strings.HasPrefix(region, "eu-"):
		vendor = "aws"
	}
	return vendor, region, instanceType
}

func estimateMonthlyUSD(vendor, region, instanceType string) string {
	catalog := map[string]float64{
		"aws/t4g.medium":        24.53,
		"aws/t4g.large":         49.06,
		"aws/m6i.large":         70.08,
		"aws/m6i.xlarge":        140.16,
		"gcp/e2-standard-2":     48.92,
		"gcp/e2-standard-4":     97.83,
		"azure/Standard_D2s_v3": 70.08,
	}
	key := strings.ToLower(vendor + "/" + instanceType)
	if v, ok := catalog[key]; ok {
		return fmt.Sprintf("$%.2f/mo", v)
	}
	if instanceType == "" {
		return "unknown"
	}
	return "unknown (" + instanceType + " in " + region + ")"
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func severityRank(value string) int {
	switch strings.ToLower(value) {
	case "critical":
		return 4
	case "high":
		return 3
	case "medium":
		return 2
	case "low":
		return 1
	default:
		return 0
	}
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func dedupe(findings []Finding) []Finding {
	seen := map[string]bool{}
	out := []Finding{}
	for _, f := range findings {
		key := f.ID
		if key == "" {
			key = f.Namespace + "/" + f.ResourceKind + "/" + f.ResourceName + "/" + f.Status
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, f)
	}
	return out
}

func summarizeScan(findings []Finding, skipped []SkippedCheck) ScanSummary {
	summary := ScanSummary{Findings: len(findings), SkippedChecks: len(skipped)}
	for _, finding := range findings {
		switch strings.ToLower(finding.Severity) {
		case "high":
			summary.HighSeverity++
		case "medium":
			summary.MediumSeverity++
		case "low":
			summary.LowSeverity++
		}
	}
	return summary
}

var manifestPathRE = regexp.MustCompile(`(?i)\.(ya?ml|json)$`)

func Lint(paths []string) ([]LintResult, error) {
	results := []LintResult{}
	for _, p := range paths {
		if strings.HasPrefix(p, "-") {
			continue
		}
		linted, err := lintPath(p)
		if err != nil {
			results = append(results, LintResult{Path: p, Severity: "error", Message: err.Error()})
			continue
		}
		results = append(results, linted...)
	}
	return results, nil
}

func lintPath(p string) ([]LintResult, error) {
	info, err := os.Stat(p)
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		results := []LintResult{}
		err := filepath.WalkDir(p, func(item string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil || entry.IsDir() {
				return walkErr
			}
			if !isLintablePath(item) {
				return nil
			}
			linted, err := lintFile(item)
			if err != nil {
				results = append(results, LintResult{Path: item, Severity: "error", Message: err.Error()})
				return nil
			}
			results = append(results, linted...)
			return nil
		})
		if err != nil {
			return nil, err
		}
		if len(results) == 0 {
			results = append(results, LintResult{Path: p, Severity: "info", Message: "no manifest files found"})
		}
		return results, nil
	}
	if !isLintablePath(p) {
		return []LintResult{{Path: p, Severity: "info", Message: "skipped non-manifest path"}}, nil
	}
	return lintFile(p)
}

func isLintablePath(p string) bool {
	base := path.Base(p)
	return manifestPathRE.MatchString(p) || base == "Chart.yaml" || base == "kustomization.yaml" || base == "kustomization.yml"
}

func lintFile(p string) ([]LintResult, error) {
	data, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	text := string(data)
	results := []LintResult{}
	add := func(severity, message string) {
		results = append(results, LintResult{Path: p, Severity: severity, Message: message})
	}
	lower := strings.ToLower(text)
	if strings.Contains(lower, "privileged: true") {
		add("high", "privileged containers should be avoided in production unless explicitly justified")
	}
	if strings.Contains(lower, "hostpath:") {
		add("high", "hostPath volumes couple pods to nodes and can expose host files")
	}
	for _, image := range imageRefs(text) {
		switch {
		case strings.HasSuffix(image, ":latest"):
			add("medium", "image uses mutable latest tag: "+image)
		case !strings.Contains(path.Base(image), ":") && !strings.Contains(image, "@sha256:"):
			add("medium", "image should be pinned by tag or digest: "+image)
		}
	}
	if strings.Contains(lower, "containers:") {
		if !strings.Contains(lower, "resources:") {
			add("medium", "containers should define resource requests and limits")
		}
		if !strings.Contains(lower, "readinessprobe:") {
			add("low", "workload containers should define readiness probes where traffic safety matters")
		}
		if !strings.Contains(lower, "livenessprobe:") {
			add("low", "workload containers should define liveness probes for self-healing workloads")
		}
	}
	if strings.Contains(lower, "kind: deployment") && !strings.Contains(lower, "strategy:") {
		add("low", "deployment should declare an explicit rollout strategy")
	}
	if len(results) == 0 {
		add("ok", "no production lint findings")
	}
	return results, nil
}

func imageRefs(text string) []string {
	out := []string{}
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "image:") {
			continue
		}
		image := strings.TrimSpace(strings.TrimPrefix(trimmed, "image:"))
		image = strings.Trim(image, `"'`)
		if image != "" {
			out = append(out, image)
		}
	}
	return out
}
