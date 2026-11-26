package cli

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/fixora/kubectl-fixora/internal/ai"
	"github.com/fixora/kubectl-fixora/internal/kube"
	"github.com/fixora/kubectl-fixora/internal/output"
)

const bareTargetCandidateLimit = 50

// runBarePodRequest handles the roadmap's bare pod-name and natural-language
// forms. Unknown single-word commands keep the normal unknown-command message
// unless that word exactly names a live Pod.
func runBarePodRequest(ctx context.Context, stdout, stderr io.Writer, opts options, k kube.Kubectl, reader kube.Reader, first string, rest []string) (int, bool) {
	request := strings.TrimSpace(strings.Join(append([]string{first}, rest...), " "))
	if request == "" || strings.HasPrefix(request, "-") || strings.Contains(request, "/") {
		return 0, false
	}
	words := strings.Fields(request)
	pods, err := k.GetPods(ctx, opts.namespace, opts.allNS)
	if err != nil {
		if len(words) > 1 {
			fmt.Fprintf(stderr, "error: cannot list pods to resolve that request: %v\n", err)
			return 1, true
		}
		return 0, false
	}

	if len(words) == 1 {
		matches := make([]ai.IntentCandidate, 0, 1)
		podByID := make(map[string]kube.Pod)
		for _, pod := range pods.Items {
			if pod.Metadata.Name != request {
				continue
			}
			candidate := barePodCandidate(pod)
			matches = append(matches, candidate)
			podByID[candidate.ID] = pod
		}
		if len(matches) == 0 {
			return 0, false
		}
		selected, code := chooseBarePod(stdout, stderr, opts, matches, false)
		if code != 0 || selected == nil {
			return code, true
		}
		return runBarePodFix(ctx, stdout, stderr, opts, reader, k, podByID[selected.ID], false), true
	}

	if opts.noAI {
		fmt.Fprintln(stderr, "error: natural-language pod lookup requires AI; use an exact pod name with --no-ai")
		return 2, true
	}
	if len(pods.Items) == 0 {
		fmt.Fprintln(stdout, "No pods were found in the selected namespace; no diagnosis started.")
		return 0, true
	}
	if len(pods.Items) > bareTargetCandidateLimit {
		fmt.Fprintf(stderr, "error: found %d pods; narrow the namespace to %d or fewer before using a natural-language request\n", len(pods.Items), bareTargetCandidateLimit)
		return 2, true
	}
	client, err := ai.NewFromEnv()
	if err != nil {
		fmt.Fprintf(stderr, "error: natural-language pod lookup needs a configured AI provider: %s\n", ai.FailureSummary(err))
		return 1, true
	}
	if client.Provider == "noop" {
		fmt.Fprintln(stderr, "error: natural-language pod lookup needs an AI provider; the noop provider cannot match targets")
		return 1, true
	}
	candidates := make([]ai.IntentCandidate, 0, len(pods.Items))
	podByID := make(map[string]kube.Pod, len(pods.Items))
	for _, pod := range pods.Items {
		candidate := barePodCandidate(pod)
		candidates = append(candidates, candidate)
		podByID[candidate.ID] = pod
	}
	fmt.Fprintf(stderr, "Using AI provider %s (%s) to match your request against pod names and status only.\n", client.Provider, client.Model)
	intent, err := client.ResolveTarget(ctx, request, candidates)
	if err != nil {
		fmt.Fprintf(stderr, "error: could not match that request to a pod: %s\n", ai.FailureSummary(err))
		return 1, true
	}
	matched := make([]ai.IntentCandidate, 0, len(intent.CandidateIDs))
	byID := make(map[string]ai.IntentCandidate, len(candidates))
	for _, candidate := range candidates {
		byID[candidate.ID] = candidate
	}
	for _, id := range intent.CandidateIDs {
		candidate, ok := byID[id]
		if !ok {
			fmt.Fprintln(stderr, "error: AI selected a pod outside the live candidate list; no diagnosis started")
			return 1, true
		}
		matched = append(matched, candidate)
	}
	if len(matched) == 0 {
		fmt.Fprintf(stdout, "No pod matched that request: %s\nNo diagnosis started.\n", intent.Interpretation)
		return 0, true
	}
	if len(matched) > 1 && opts.output == "text" {
		fmt.Fprintf(stdout, "Fixora found %d possible pods: %s\n", len(matched), intent.Interpretation)
	}
	selected, code := chooseBarePod(stdout, stderr, opts, matched, true)
	if code != 0 || selected == nil {
		return code, true
	}
	pod, ok := podByID[selected.ID]
	if !ok {
		fmt.Fprintln(stderr, "error: selected pod is no longer in the live candidate list; no diagnosis started")
		return 1, true
	}
	if !confirmBarePod(stdout, opts, *selected) {
		return 0, true
	}
	return runBarePodFix(ctx, stdout, stderr, opts, reader, k, pod, true), true
}

func barePodCandidate(pod kube.Pod) ai.IntentCandidate {
	status := strings.TrimSpace(pod.Status.Reason)
	if status == "" {
		status = strings.TrimSpace(pod.Status.Phase)
	}
	if status == "" {
		status = "Unknown"
	}
	parts := []string{"phase=" + firstArg([]string{pod.Status.Phase}, "Unknown")}
	if pod.Status.Reason != "" && pod.Status.Reason != pod.Status.Phase {
		parts = append(parts, "reason="+pod.Status.Reason)
	}
	restarts := 0
	for _, cs := range append(append([]kube.ContainerStatus{}, pod.Status.InitStatuses...), pod.Status.ContainerStatuses...) {
		restarts += cs.RestartCount
		for stateName, state := range cs.State {
			if state.Reason != "" {
				status = state.Reason
			} else if stateName == "waiting" || stateName == "terminated" {
				status = stateName
			}
			parts = append(parts, cs.Name+"="+firstArg([]string{state.Reason}, stateName))
		}
	}
	parts = append(parts, fmt.Sprintf("restarts=%d", restarts))
	return ai.IntentCandidate{
		ID:           pod.Metadata.Namespace + "/" + pod.Metadata.Name,
		Namespace:    pod.Metadata.Namespace,
		ResourceKind: "Pod",
		ResourceName: pod.Metadata.Name,
		Status:       status,
		Summary:      strings.Join(parts, "; "),
	}
}

func chooseBarePod(stdout, stderr io.Writer, opts options, matches []ai.IntentCandidate, aiMatched bool) (*ai.IntentCandidate, int) {
	if len(matches) == 0 {
		return nil, 0
	}
	if opts.askSelect != "" {
		choice, err := strconv.Atoi(opts.askSelect)
		if err != nil || choice < 1 || choice > len(matches) {
			fmt.Fprintf(stderr, "error: --select must be a number from 1 to %d for the pod candidates\n", len(matches))
			return nil, 2
		}
		if aiMatched {
			printBarePod(stdout, opts, matches[choice-1], true)
		}
		return &matches[choice-1], 0
	}
	if len(matches) == 1 {
		if aiMatched {
			printBarePod(stdout, opts, matches[0], true)
		}
		return &matches[0], 0
	}
	if opts.output != "text" {
		_ = output.Write(stdout, opts.output, map[string]any{"status": "ambiguous", "candidates": matches, "noChangesMade": true})
		return nil, 0
	}
	if !opts.interactive {
		for i, candidate := range matches {
			fmt.Fprintf(stdout, "  %d. %s/%s (%s) — %s\n", i+1, candidate.Namespace, candidate.ResourceName, candidate.Status, candidate.Summary)
		}
		fmt.Fprintln(stdout, "Choose a pod with --select <number>; no diagnosis started.")
		return nil, 0
	}
	for i, candidate := range matches {
		fmt.Fprintf(stdout, "  %d. %s/%s (%s) — %s\n", i+1, candidate.Namespace, candidate.ResourceName, candidate.Status, candidate.Summary)
	}
	fmt.Fprint(stdout, "Select the pod to inspect (1-N): ")
	line, err := bufio.NewReader(inputFor(opts)).ReadString('\n')
	if err != nil && strings.TrimSpace(line) == "" {
		fmt.Fprintf(stderr, "error: could not read pod selection: %v\n", err)
		return nil, 2
	}
	choice, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil || choice < 1 || choice > len(matches) {
		fmt.Fprintln(stderr, "error: invalid pod selection")
		return nil, 2
	}
	return &matches[choice-1], 0
}

func printBarePod(stdout io.Writer, opts options, candidate ai.IntentCandidate, aiMatched bool) {
	if opts.output != "text" {
		return
	}
	prefix := "Exact pod: "
	if aiMatched {
		prefix = "Possible pod: "
	}
	fmt.Fprintf(stdout, "%s%s/%s (%s) — %s\n", prefix, candidate.Namespace, candidate.ResourceName, candidate.Status, candidate.Summary)
}

func confirmBarePod(stdout io.Writer, opts options, candidate ai.IntentCandidate) bool {
	if opts.confirmTarget {
		return true
	}
	if opts.output != "text" || !opts.interactive {
		if opts.output == "text" {
			fmt.Fprintln(stdout, "Confirm the target with --confirm-target to continue; no diagnosis started.")
		} else {
			_ = output.Write(stdout, opts.output, map[string]any{"status": "target-confirmation-required", "target": candidate, "noChangesMade": true, "next": "Rerun with --confirm-target after reviewing the selected pod."})
		}
		return false
	}
	fmt.Fprintf(stdout, "Is %s/%s the pod you meant? [y/N]: ", candidate.Namespace, candidate.ResourceName)
	line, err := bufio.NewReader(inputFor(opts)).ReadString('\n')
	if err != nil && strings.TrimSpace(line) == "" {
		fmt.Fprintln(stdout, "No diagnosis started.")
		return false
	}
	answer := strings.ToLower(strings.TrimSpace(line))
	if answer != "y" && answer != "yes" {
		fmt.Fprintln(stdout, "No diagnosis started.")
		return false
	}
	return true
}

func runBarePodFix(ctx context.Context, stdout, stderr io.Writer, opts options, reader kube.Reader, k kube.Kubectl, pod kube.Pod, aiSelected bool) int {
	if pod.Metadata.Namespace == "" || pod.Metadata.Name == "" {
		fmt.Fprintln(stderr, "error: selected pod has incomplete identity; no diagnosis started")
		return 1
	}
	opts.namespace = pod.Metadata.Namespace
	opts.allNS = false
	resource := "pod/" + pod.Metadata.Name
	applyWorkflowDefaults("fix", &opts)
	if aiSelected {
		if opts.noAI {
			fmt.Fprintln(stderr, "error: --no-ai cannot be used with a natural-language pod request")
			return 2
		}
		opts.useAI = true
		if !opts.unsafeAI {
			opts.redact = true
		}
		if opts.output == "text" {
			fmt.Fprintf(stdout, "Target confirmed: Pod/%s (%s)\n", pod.Metadata.Name, pod.Metadata.Namespace)
		}
	} else if !opts.visited["ai"] {
		// The bare exact-name form is deterministic by default; --ai is still an
		// explicit opt-in for diagnosis after the local target match.
		opts.useAI = false
	}
	reconcileDeliveryFlags(&opts, stderr)
	fmt.Fprintf(stderr, "Analyzing Pod/%s in namespace %s...\n", pod.Metadata.Name, pod.Metadata.Namespace)
	analysisCtx, cancel := fixAnalysisContext(ctx, opts.timeout)
	defer cancel()
	finding, plan, prepared, err := prepareFixPlan(analysisCtx, reader, k, opts, resource, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "error preparing pod diagnosis: %v\n", err)
		return 1
	}
	return runPreparedFix(ctx, stdout, stderr, prepared, reader, k, finding, plan, resource)
}
