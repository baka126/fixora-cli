package analyzer

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/fixora/kubectl-fixora/internal/config"
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
				skipped = append(skipped, rbacAwareSkip("pods", err))
				mu.Unlock()
				return
			}
			events, err := sctx.GetEvents()
			if err != nil {
				mu.Lock()
				skipped = append(skipped, rbacAwareSkip("events", err))
				mu.Unlock()
				events = nil
			}

			eventIndex := make(map[string][]kube.Event, len(events))
			for _, event := range events {
				ref := event.InvolvedObject
				if ref.Kind != "Pod" || ref.Name == "" {
					continue
				}
				key := firstNonEmpty(ref.Namespace, event.Metadata.Namespace) + "/" + ref.Name
				eventIndex[key] = append(eventIndex[key], event)
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

						relatedEvents := eventsForPod(eventIndex[p.Metadata.Namespace+"/"+p.Metadata.Name], p)
						finding, ok := a.findingForPod(workerCtx, sctx, p, relatedEvents)
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
	provider := os.Getenv("FIXORA_AI_PROVIDER")
	if provider == "" {
		if cfg, err := config.Load(); err == nil && cfg.AIProvider != "" {
			provider = cfg.AIProvider
		} else {
			provider = "local"
		}
	}
	return ScanEnvelope{
		APIVersion: "fixora.dev/v1alpha1",
		Kind:       "AnalysisReport",
		Status:     status,
		Provider:   provider,
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
		finding, ok := a.findingForPod(ctx, nil, pod, events)
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
	sctx := NewScanContext(ctx, a.k, a.opts)
	for _, pod := range related {
		podEvents := eventsForPod(events, pod)
		if pf, ok := a.findingForPod(ctx, sctx, pod, podEvents); ok {
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
		ref := event.InvolvedObject
		namespace := firstNonEmpty(ref.Namespace, event.Metadata.Namespace)
		if ref.Kind != "Pod" || namespace != pod.Metadata.Namespace || ref.Name != pod.Metadata.Name {
			continue
		}
		if ref.UID != "" && (pod.Metadata.UID == "" || ref.UID != pod.Metadata.UID) {
			continue
		}
		out = append(out, event)
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

func (a Analyzer) findingForPod(ctx context.Context, sctx *ScanContext, pod kube.Pod, events []kube.Event) (Finding, bool) {
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
	for _, container := range append(append([]kube.Container{}, pod.Spec.InitContainers...), pod.Spec.Containers...) {
		if container.Name != "" && container.Image != "" {
			f.Evidence = append(f.Evidence, Evidence{Label: "Container image " + container.Name, Value: container.Image})
		}
	}

	// A Pending/Unschedulable pod whose summed requests exceed every node's
	// allocatable is fixable by lowering the requests (the resources strategy).
	// Taints, affinity and PVC binding are also Pending causes, are not fixed
	// this way, and deliberately produce no evidence here so they keep falling
	// through to the review-only scheduling strategy.
	if status == "Pending" || status == "Unschedulable" {
		if evidence, ok := a.allocatableShortfall(ctx, sctx, pod); ok {
			f.Evidence = append(f.Evidence, evidence)
		}
	}

	// A CreateContainerConfigError pod references a ConfigMap or Secret that
	// does not resolve. Name the env var and key behind the reference so the
	// env inferrer can rebuild it once it identifies the intended object.
	if status == "CreateContainerConfigError" {
		f.Evidence = append(f.Evidence, envRefEvidence(pod)...)
	}

	// A ProbeFailure pod is Running but never Ready. Record the ports the
	// not-ready container declares and the port its readiness probe targets so
	// the probe inferrer can propose the declared port when they disagree.
	if status == "ProbeFailure" {
		f.Evidence = append(f.Evidence, probePortEvidence(pod)...)
	}

	// An ImagePullBackOff pod never starts a container, so there are no logs to
	// classify and the log-driven ExecFormatError path below never runs for it.
	// The image inferrer and the AI patch path both need node-platform evidence
	// to rank platform-compatible replacement images, so attach it here.
	if strings.Contains(status, "ImagePull") {
		a.appendNodePlatformEvidence(ctx, sctx, &f, pod.Spec.NodeName)
	}

	corr, recent := CorrelateRecentEvents(events)
	f.ChangeCorrelation = corr
	f.RecentChanges = recent

	for _, event := range events {
		f.Evidence = append(f.Evidence, Evidence{Label: "Event " + event.Reason, Value: event.Message})
	}
	if a.opts.IncludeLogs {
		if logs, err := a.k.Logs(ctx, pod.Metadata.Namespace, pod.Metadata.Name, false); err == nil && logs != "" {
			f.Logs = append(f.Logs, LogSnippet{Source: "current", Text: AggregateLogs(a.redact(logs))})
		}
		if logs, err := a.k.Logs(ctx, pod.Metadata.Namespace, pod.Metadata.Name, true); err == nil && logs != "" {
			f.Logs = append(f.Logs, LogSnippet{Source: "previous", Text: AggregateLogs(a.redact(logs))})
		}
	}

	// Refine diagnostics from logs via the deterministic signal classifier.
	// Current logs are authoritative; previous logs drive the recurrence flag.
	var current, previous string
	for _, l := range f.Logs {
		switch l.Source {
		case "current":
			current += l.Text + "\n"
		case "previous":
			previous += l.Text + "\n"
		}
	}
	if sig, match, ok := classifyLogSignal(current, previous); ok {
		f.Status = sig.status
		f.Summary = sig.summary
		f.Category = sig.category
		f.Recommendations = []Recommendation{sig.rec}
		if sig.status == "ExecFormatError" {
			a.appendNodePlatformEvidence(ctx, sctx, &f, pod.Spec.NodeName)
		}
		recurrence := "matched in current logs only — new since the last restart"
		if match.Recurring {
			recurrence = "matched in both current and previous logs — persists across restarts (not transient)"
		} else if match.Previous {
			recurrence = "matched in previous logs only — last failed run evidence; current logs did not repeat it"
		}
		f.Evidence = append(f.Evidence, Evidence{Label: "Log recurrence", Value: recurrence})
	}
	// Keep the identity aligned with the final status: the log-refinement above
	// can change f.Status after the ID was first built.
	f.ID = pod.Metadata.Namespace + "/" + pod.Metadata.Name + "/" + f.Status

	// Transient-recovery guard: a pod that is currently Running with all
	// containers Ready has already recovered; its finding came from a historical
	// trigger (e.g. OOMKilled from LastState). Keep the diagnosis but lower its
	// urgency — the planner blocks auto-apply for recovered findings.
	if pod.Status.Phase == "Running" && allContainersReady(pod) {
		f.Recovered = true
		f.Severity = "low"
		f.Evidence = append(f.Evidence, Evidence{
			Label: "Observed recovered",
			Value: fmt.Sprintf("all containers Ready; restarts=%d — the pod is currently healthy. Confirm the cause will recur before applying a fix.", totalRestarts(pod)),
		})
	}

	return f, true
}

// allocatableShortfall reports requests-exceed-allocatable evidence when no
// node could satisfy the pod's summed memory or CPU requests. Only this
// sub-case of Pending is fixable by lowering requests. The evidence value
// carries both allocatable figures in a form internal/infer's parseAllocatable
// can read back — keep the two in sync.
func (a Analyzer) allocatableShortfall(ctx context.Context, sctx *ScanContext, pod kube.Pod) (Evidence, bool) {
	var reqMem, reqCPU int64
	for _, container := range append(append([]kube.Container{}, pod.Spec.InitContainers...), pod.Spec.Containers...) {
		if mi, ok := quantityMi(container.Resources.Requests["memory"]); ok {
			reqMem += mi
		}
		if m, ok := milliCores(container.Resources.Requests["cpu"]); ok {
			reqCPU += m
		}
	}
	if reqMem == 0 && reqCPU == 0 {
		return Evidence{}, false
	}
	nodes, err := a.nodeList(ctx, sctx)
	if err != nil {
		return Evidence{}, false
	}
	var largestMem, largestCPU int64
	for _, node := range nodes {
		if mi, ok := quantityMi(node.Status.Allocatable["memory"]); ok && mi > largestMem {
			largestMem = mi
		}
		if m, ok := milliCores(node.Status.Allocatable["cpu"]); ok && m > largestCPU {
			largestCPU = m
		}
	}
	// Both figures are needed to size a complete patch; without either, the
	// inferrer could not fill every placeholder, so decline here too.
	if largestMem == 0 || largestCPU == 0 {
		return Evidence{}, false
	}
	if reqMem <= largestMem && reqCPU <= largestCPU {
		return Evidence{}, false
	}
	return Evidence{
		Label: "Requests exceed node allocatable",
		Value: fmt.Sprintf("requested memory=%dMi cpu=%dm; largest node allocatable memory=%dMi cpu=%dm",
			reqMem, reqCPU, largestMem, largestCPU),
	}, true
}

// envRefEvidence names the env var and key behind a ConfigMap or Secret
// reference, so the env inferrer can rebuild the reference once it identifies
// the intended object.
func envRefEvidence(pod kube.Pod) []Evidence {
	for _, container := range append(append([]kube.Container{}, pod.Spec.InitContainers...), pod.Spec.Containers...) {
		for _, env := range container.Env {
			for _, refKind := range []string{"configMapKeyRef", "secretKeyRef"} {
				ref, ok := env.ValueFrom[refKind].(map[string]any)
				if !ok {
					continue
				}
				key, _ := ref["key"].(string)
				name, _ := ref["name"].(string)
				kind := "ConfigMap"
				if refKind == "secretKeyRef" {
					kind = "Secret"
				}
				return []Evidence{
					{Label: "Env reference name", Value: env.Name},
					{Label: "Env reference key", Value: key},
					{Label: "Env reference kind", Value: kind},
					{Label: "Env reference object", Value: name},
				}
			}
		}
	}
	return nil
}

// probePortEvidence records the ports the not-ready container declares and the
// port its readiness probe targets — the two values the probe inferrer
// compares.
func probePortEvidence(pod kube.Pod) []Evidence {
	notReady := map[string]bool{}
	for _, cs := range pod.Status.ContainerStatuses {
		if !cs.Ready {
			notReady[cs.Name] = true
		}
	}
	for _, container := range pod.Spec.Containers {
		if !notReady[container.Name] {
			continue
		}
		ports := make([]string, 0, len(container.Ports))
		for _, port := range container.Ports {
			ports = append(ports, strconv.Itoa(port.ContainerPort))
		}
		return []Evidence{
			{Label: "Container ports", Value: strings.Join(ports, ",")},
			{Label: "Readiness probe port", Value: probeTargetPort(container.ReadinessProbe)},
		}
	}
	return nil
}

// probeTargetPort pulls the port out of an httpGet or tcpSocket probe. The
// probe arrives as decoded JSON, so a numeric port is a float64.
func probeTargetPort(probe map[string]any) string {
	for _, handler := range []string{"httpGet", "tcpSocket"} {
		h, ok := probe[handler].(map[string]any)
		if !ok {
			continue
		}
		switch port := h["port"].(type) {
		case string:
			return port
		case float64:
			return strconv.Itoa(int(port))
		case int:
			return strconv.Itoa(port)
		}
	}
	return ""
}

// quantityMi converts a Kubernetes memory quantity (Ki/Mi/Gi) to mebibytes.
func quantityMi(value string) (int64, bool) {
	value = strings.TrimSpace(value)
	switch {
	case strings.HasSuffix(value, "Gi"):
		n, err := strconv.ParseInt(strings.TrimSuffix(value, "Gi"), 10, 64)
		return n * 1024, err == nil
	case strings.HasSuffix(value, "Mi"):
		n, err := strconv.ParseInt(strings.TrimSuffix(value, "Mi"), 10, 64)
		return n, err == nil
	case strings.HasSuffix(value, "Ki"):
		n, err := strconv.ParseInt(strings.TrimSuffix(value, "Ki"), 10, 64)
		return n / 1024, err == nil
	}
	return 0, false
}

// milliCores converts a Kubernetes CPU quantity to millicores. It accepts a
// millicore suffix ("100m"), a bare integer core count ("8", "100"), and a
// fractional core count ("0.5").
func milliCores(value string) (int64, bool) {
	value = strings.TrimSpace(value)
	switch {
	case value == "":
		return 0, false
	case strings.HasSuffix(value, "m"):
		n, err := strconv.ParseInt(strings.TrimSuffix(value, "m"), 10, 64)
		return n, err == nil && n >= 0
	default:
		cores, err := strconv.ParseFloat(value, 64)
		return int64(cores * 1000), err == nil && cores >= 0
	}
}

// totalRestarts sums restartCount across init and app container statuses.
func totalRestarts(pod kube.Pod) int {
	total := 0
	for _, cs := range pod.Status.InitStatuses {
		total += cs.RestartCount
	}
	for _, cs := range pod.Status.ContainerStatuses {
		total += cs.RestartCount
	}
	return total
}

// nodeList returns the cluster nodes, using the per-scan cache when a
// ScanContext is available so concurrent pod workers don't each issue a
// cluster-wide node listing.
func (a Analyzer) nodeList(ctx context.Context, sctx *ScanContext) ([]kube.Node, error) {
	if sctx != nil {
		return sctx.GetNodes()
	}
	return a.k.GetNodes(ctx)
}

func (a Analyzer) appendNodePlatformEvidence(ctx context.Context, sctx *ScanContext, finding *Finding, nodeName string) {
	if finding == nil || strings.TrimSpace(nodeName) == "" {
		return
	}
	nodes, err := a.nodeList(ctx, sctx)
	if err != nil {
		finding.Evidence = append(finding.Evidence, Evidence{Label: "Node platform lookup", Value: err.Error()})
		return
	}
	for _, node := range nodes {
		if node.Metadata.Name != nodeName {
			continue
		}
		architecture := firstNonEmpty(node.Metadata.Labels["kubernetes.io/arch"], node.Metadata.Labels["beta.kubernetes.io/arch"])
		osName := firstNonEmpty(node.Metadata.Labels["kubernetes.io/os"], node.Metadata.Labels["beta.kubernetes.io/os"], "linux")
		if architecture != "" {
			finding.Evidence = append(finding.Evidence, Evidence{Label: "Node platform", Value: osName + "/" + architecture})
		}
		return
	}
	finding.Evidence = append(finding.Evidence, Evidence{Label: "Node platform lookup", Value: "node not returned by API"})
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
		if term, ok := cs.State["terminated"]; ok {
			switch {
			case strings.Contains(term.Reason, "OOMKilled"):
				return "OOMKilled", "resources", "high"
			case cs.RestartCount >= 1 && (term.ExitCode != 0 || term.Reason == "Error"):
				// A container currently reported as terminated with a non-zero
				// exit that has already restarted is crash-looping. Recent
				// kubelet reports this state for most of the back-off period
				// instead of waiting: CrashLoopBackOff, so keying only off the
				// waiting reason misses an active crash loop between restarts.
				return "CrashLoopBackOff", "runtime", "critical"
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
	// Running but never Ready is a failing readiness probe. Checked after the
	// container-state switches above so a crash-looping or image-pull-failing
	// container keeps its own classification — those are also never Ready.
	// Gated on the container's own probe window so a pod still inside its
	// startup delay, or one shutting down, is not misread as a misconfiguration.
	if pod.Status.Phase == "Running" {
		for _, cs := range pod.Status.ContainerStatuses {
			if _, running := cs.State["running"]; running && !cs.Ready {
				if probeFailureConfirmed(pod, cs.Name, time.Now()) {
					return "ProbeFailure", "runtime", "high"
				}
			}
		}
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

// probeFailureConfirmed reports whether a Running, not-Ready container has had
// long enough for its own readiness probe to have failed for a real reason
// rather than the pod still being inside its startup window. A pod whose
// deletion has been requested is excluded: a not-Ready container there is
// usually a preStop hook draining, not a misconfiguration.
func probeFailureConfirmed(pod kube.Pod, container string, now time.Time) bool {
	if strings.TrimSpace(pod.Metadata.DeletionTimestamp) != "" {
		return false
	}
	created, err := time.Parse(time.RFC3339, pod.Metadata.CreationTimestamp)
	if err != nil {
		// No parseable creation time: cannot age-gate the probe window, so
		// fail closed rather than risk a spurious ProbeFailure driving an
		// apply-eligible patch. The API server stamps creationTimestamp on
		// every persisted pod, so this is unreachable for real objects.
		return false
	}
	return now.Sub(created) >= probeReadyWindow(pod, container)
}

// probeReadyWindow is how long the container's readiness probe is allowed to
// keep failing before a persistent not-Ready state counts as a
// misconfiguration: initialDelaySeconds + periodSeconds × failureThreshold read
// from the container's own readinessProbe. Unset fields fall back to the
// Kubernetes probe defaults (period 10, failureThreshold 3), so a probe with no
// timings — or no probe at all — yields the 30s default.
func probeReadyWindow(pod kube.Pod, container string) time.Duration {
	initial, period, failures := 0, 10, 3
	for _, c := range pod.Spec.Containers {
		if c.Name != container {
			continue
		}
		if p := c.ReadinessProbe; len(p) > 0 {
			initial = probeInt(p, "initialDelaySeconds", initial)
			period = probeInt(p, "periodSeconds", period)
			failures = probeInt(p, "failureThreshold", failures)
		}
		break
	}
	w := time.Duration(initial+period*failures) * time.Second
	if w <= 0 {
		return 30 * time.Second
	}
	return w
}

func probeInt(probe map[string]any, key string, def int) int {
	if _, ok := probe[key]; !ok {
		return def
	}
	return intValue(probe[key])
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
	case strings.Contains(status, "ProbeFailure"):
		return []Recommendation{{Title: "Correct the readiness probe", Description: "Check the probe's port, path, scheme and timing against the port the container actually listens on.", PatchType: "probe", SafeByDefault: true}}
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
	case strings.Contains(status, "ProbeFailure"):
		return "Container is running but never becomes Ready; its readiness probe keeps failing."
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
