package shadow

import (
	"context"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/fixora/kubectl-fixora/internal/kube"
)

func verifyClone(ctx context.Context, c *kube.TypedClient, namespace, name string, timeout time.Duration, attempt int, allowCompletion bool) Attempt {
	return verifyCloneWithWindows(ctx, c, namespace, name, timeout, attempt, allowCompletion, startupWindow, soakWindow(allowCompletion))
}

func verifyCloneWithWindows(ctx context.Context, c *kube.TypedClient, namespace, name string, timeout time.Duration, attempt int, allowCompletion bool, startupAllowance, stabilityWindow time.Duration) Attempt {
	return verifyCloneWithEvidence(ctx, c, namespace, name, timeout, attempt, allowCompletion, nil, nil, startupAllowance, stabilityWindow)
}

func verifyCloneWithSource(ctx context.Context, c *kube.TypedClient, namespace, name string, timeout time.Duration, attempt int, allowCompletion bool, source *corev1.Pod, evidence *FailureEvidence) Attempt {
	return verifyCloneWithEvidence(ctx, c, namespace, name, timeout, attempt, allowCompletion, source, evidence, startupWindow, soakWindow(allowCompletion))
}

func verifyCloneWithEvidence(ctx context.Context, c *kube.TypedClient, namespace, name string, timeout time.Duration, attempt int, allowCompletion bool, source *corev1.Pod, evidence *FailureEvidence, startupAllowance, stabilityWindow time.Duration) Attempt {
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	result := Attempt{Number: attempt, CloneName: name}
	if evidence == nil {
		evidence = collectFailureEvidence(ctx, c, namespace, source, nil)
	}
	result.Evidence = evidence
	memoryResults := startMemoryEvidenceSampler(ctx, c, namespace, name, result.Evidence)
	defer func() {
		if result.Evidence != nil && result.Evidence.MemoryMetricsStatus == "pending" {
			result.Evidence.MemoryMetricsStatus = "not-observed"
		}
	}()
	verificationStarted := time.Now()
	var startupTimer *time.Timer
	var startupDone <-chan time.Time
	if !allowCompletion {
		startupTimer = time.NewTimer(startupAllowance)
		defer startupTimer.Stop()
		startupDone = startupTimer.C
	}
	var stableSince time.Time
	var stableDone <-chan time.Time
	resourceVersion := ""
	const baselineRestarts = 0
	if pod, err := c.GetTypedPod(ctx, namespace, name); err == nil {
		resourceVersion = pod.ResourceVersion
		updateAttemptFromPod(&result, pod)
		updateCandidateEvidence(result.Evidence, pod, time.Now())
		if pod.Status.Phase == corev1.PodSucceeded {
			if allowCompletion {
				result.Ready = true
				result.Message = "shadow batch pod completed successfully"
				return result
			}
			result.Message = "shadow pod completed before becoming ready"
			result.Ready = false
			enrichFailure(c, namespace, name, &result)
			return result
		}
		if terminalFailure(result.ExitReason) || pod.Status.Phase == corev1.PodFailed {
			result.Ready = false
			enrichFailure(c, namespace, name, &result)
			return result
		}
		if result.Restarts > baselineRestarts {
			result.Ready = false
			result.Message = fmt.Sprintf("container restarted during startup (%d restarts)", result.Restarts)
			enrichFailure(c, namespace, name, &result)
			return result
		}
		if pod.Status.Phase == corev1.PodRunning && podReady(pod) {
			if !allowCompletion {
				within, known := initialReadyWithinWindow(pod, time.Now(), verificationStarted, startupAllowance)
				if !within {
					result.Ready = false
					result.Inconclusive = !known
					result.Message = "pod did not become Ready within the startup allowance"
					if result.Inconclusive {
						result.Message = "startup allowance ended before Fixora could establish when the pod became Ready"
					}
					return result
				}
				stableSince = time.Now()
				stableDone = time.After(stabilityWindow)
				startupTimer.Stop()
				startupDone = nil
			}
		}
	}
	watcher, err := c.WatchPod(ctx, namespace, name, resourceVersion)
	if err != nil {
		result.Ready = false
		result.Message = err.Error()
		result.Inconclusive = true
		return result
	}
	defer watcher.Stop()

	for {
		select {
		case <-ctx.Done():
			result.Ready = false
			result.Message = "verification ended before the startup and stability checks completed"
			result.Inconclusive = true
			enrichFailure(c, namespace, name, &result)
			return result
		case <-startupDone:
			if stableSince.IsZero() {
				pod, err := c.GetTypedPod(ctx, namespace, name)
				if err != nil {
					result.Ready = false
					result.Inconclusive = true
					result.Message = "could not confirm startup state at the end of the allowance: " + err.Error()
					return result
				}
				updateAttemptFromPod(&result, pod)
				updateCandidateEvidence(result.Evidence, pod, time.Now())
				within, known := initialReadyWithinWindow(pod, time.Now(), verificationStarted, startupAllowance)
				if pod.Status.Phase == corev1.PodRunning && podReady(pod) && within {
					stableSince = time.Now()
					stableDone = time.After(stabilityWindow)
					startupDone = nil
					continue
				}
				result.Ready = false
				result.Inconclusive = !known && podReady(pod)
				result.Message = "pod did not become Ready within the startup allowance"
				if result.Inconclusive {
					result.Message = "startup allowance ended before Fixora could establish when the pod became Ready"
				}
				enrichFailure(c, namespace, name, &result)
				return result
			}
		case <-stableDone:
			pod, err := c.GetTypedPod(ctx, namespace, name)
			if err != nil {
				result.Ready = false
				result.Inconclusive = true
				result.Message = "could not confirm final stability state: " + err.Error()
				return result
			}
			updateAttemptFromPod(&result, pod)
			updateCandidateEvidence(result.Evidence, pod, time.Now())
			if reason, stable := stillStable(pod, baselineRestarts); !stable {
				result.Ready = false
				result.Message = "shadow pod did not stay ready: " + reason
				enrichFailure(c, namespace, name, &result)
				return result
			}
			result.Ready = true
			result.Message = fmt.Sprintf("shadow pod stayed ready for %s (%d restarts)", time.Since(stableSince).Round(time.Second), result.Restarts)
			return result
		case observation, ok := <-memoryResults:
			if !ok {
				memoryResults = nil
				continue
			}
			applyMemorySampleResult(result.Evidence, observation)
		case event, ok := <-watcher.ResultChan():
			if !ok {
				result.Ready = false
				result.Message = "pod watch ended before verification completed"
				result.Inconclusive = true
				enrichFailure(c, namespace, name, &result)
				return result
			}
			pod, ok := event.Object.(*corev1.Pod)
			if !ok || pod == nil {
				continue
			}
			updateAttemptFromPod(&result, pod)
			updateCandidateEvidence(result.Evidence, pod, time.Now())
			if pod.Status.Phase == corev1.PodSucceeded {
				if allowCompletion {
					result.Ready = true
					result.Message = "shadow batch pod completed successfully"
					return result
				}
				result.Message = "shadow pod completed before becoming ready"
				result.Ready = false
				enrichFailure(c, namespace, name, &result)
				return result
			}
			if !stableSince.IsZero() {
				// Soaking: any regression fails the attempt outright.
				if reason, stable := stillStable(pod, baselineRestarts); !stable {
					result.Ready = false
					result.Message = "shadow pod did not stay ready: " + reason
					enrichFailure(c, namespace, name, &result)
					return result
				}
				continue
			}
			if terminalFailure(result.ExitReason) || pod.Status.Phase == corev1.PodFailed {
				result.Ready = false
				enrichFailure(c, namespace, name, &result)
				return result
			}
			if result.Restarts > baselineRestarts {
				result.Ready = false
				result.Message = fmt.Sprintf("container restarted during startup (%d restarts)", result.Restarts)
				enrichFailure(c, namespace, name, &result)
				return result
			}
			if pod.Status.Phase == corev1.PodRunning && podReady(pod) {
				if !allowCompletion {
					within, known := initialReadyWithinWindow(pod, time.Now(), verificationStarted, startupAllowance)
					if !within {
						result.Ready = false
						result.Inconclusive = !known
						result.Message = "pod did not become Ready within the startup allowance"
						if result.Inconclusive {
							result.Message = "startup allowance ended before Fixora could establish when the pod became Ready"
						}
						enrichFailure(c, namespace, name, &result)
						return result
					}
					stableSince = time.Now()
					stableDone = time.After(stabilityWindow)
					startupTimer.Stop()
					startupDone = nil
				}
			}
		}
	}
}

func initialReadyWithinWindow(pod *corev1.Pod, observedAt, verificationStarted time.Time, allowance time.Duration) (within, known bool) {
	if pod != nil && !pod.CreationTimestamp.IsZero() {
		for _, condition := range pod.Status.Conditions {
			if condition.Type != corev1.PodReady || condition.Status != corev1.ConditionTrue || condition.LastTransitionTime.IsZero() {
				continue
			}
			readyAfter := condition.LastTransitionTime.Time.Sub(pod.CreationTimestamp.Time)
			return readyAfter >= 0 && readyAfter <= allowance, true
		}
	}
	// Fake or older APIs may omit transition timestamps. A prompt initial read
	// is usable in that case; a late read without timestamps cannot prove when
	// readiness began.
	if pod != nil && observedAt.Sub(verificationStarted) <= allowance {
		return true, true
	}
	return false, false
}

const (
	startupWindow = 60 * time.Second
	stableWindow  = 120 * time.Second
)

// soakWindow is how long a shadow clone must stay continuously ready after it
// first becomes Ready. A clone with no readiness probe reports Ready the
// instant its container starts, so a single observation cannot distinguish a
// working patch from one that crashes seconds later. Batch workloads get no
// soak: a Job clone legitimately reaches Succeeded and leaves Running.
func soakWindow(allowCompletion bool) time.Duration {
	if allowCompletion {
		return 0
	}
	return stableWindow
}

func statusForAttempt(attempt Attempt) VerificationStatus {
	if attempt.Ready {
		return VerificationVerified
	}
	if attempt.Inconclusive {
		return VerificationInconclusive
	}
	return VerificationFailed
}

// stillStable reports whether an observation during the stability window is
// still Ready and has no restart since clone creation. The returned string
// explains the break and is empty when stable.
func stillStable(pod *corev1.Pod, baselineRestarts int) (string, bool) {
	if pod.Status.Phase != corev1.PodRunning {
		return "phase left Running: " + string(pod.Status.Phase), false
	}
	if !podReady(pod) {
		return "pod stopped reporting Ready", false
	}
	restarts := 0
	for _, status := range append(pod.Status.InitContainerStatuses, pod.Status.ContainerStatuses...) {
		restarts += int(status.RestartCount)
		if status.State.Terminated != nil && terminalFailure(status.State.Terminated.Reason) {
			return "container terminated: " + status.State.Terminated.Reason, false
		}
	}
	if restarts > baselineRestarts {
		return fmt.Sprintf("container restarted during soak (%d -> %d)", baselineRestarts, restarts), false
	}
	return "", true
}

func updateAttemptFromPod(result *Attempt, pod *corev1.Pod) {
	result.Phase = string(pod.Status.Phase)
	result.Ready = podReady(pod)
	restarts := 0
	for _, status := range append(pod.Status.InitContainerStatuses, pod.Status.ContainerStatuses...) {
		restarts += int(status.RestartCount)
		if status.State.Waiting != nil && status.State.Waiting.Reason != "" {
			result.ExitReason = status.State.Waiting.Reason
		}
		if status.State.Terminated != nil && status.State.Terminated.Reason != "" {
			result.ExitReason = status.State.Terminated.Reason
		}
		if status.LastTerminationState.Terminated != nil && status.LastTerminationState.Terminated.Reason != "" {
			result.ExitReason = status.LastTerminationState.Terminated.Reason
		}
	}
	result.Restarts = restarts
}

func podReady(pod *corev1.Pod) bool {
	// A PodReady condition is authoritative: return its status directly.
	for _, cond := range pod.Status.Conditions {
		if cond.Type == corev1.PodReady {
			return cond.Status == corev1.ConditionTrue
		}
	}
	// No PodReady condition — fall back to per-container readiness.
	statuses := append(pod.Status.InitContainerStatuses, pod.Status.ContainerStatuses...)
	if len(statuses) == 0 {
		return false
	}
	for _, status := range statuses {
		if !status.Ready && status.State.Terminated == nil {
			return false
		}
	}
	return true
}

func terminalFailure(reason string) bool {
	switch reason {
	case "OOMKilled", "CrashLoopBackOff", "ImagePullBackOff", "ErrImagePull", "CreateContainerConfigError", "CreateContainerError", "RunContainerError", "Error":
		return true
	default:
		return false
	}
}

func enrichFailure(c *kube.TypedClient, namespace, name string, attempt *Attempt) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if logs, err := c.Logs(ctx, namespace, name, false); err == nil && strings.TrimSpace(logs) != "" {
		attempt.Logs = append(attempt.Logs, logs)
	}
	if logs, err := c.Logs(ctx, namespace, name, true); err == nil && strings.TrimSpace(logs) != "" {
		attempt.Logs = append(attempt.Logs, logs)
	}
	events, err := c.GetEvents(ctx, namespace, "")
	if err != nil {
		return
	}
	for _, ev := range events {
		if ev.InvolvedObject.Name == name {
			attempt.Events = append(attempt.Events, fmt.Sprintf("%s %s: %s", ev.Type, ev.Reason, ev.Message))
		}
	}
	recordProbeFailureEvents(attempt.Evidence, attempt.Events)
}
