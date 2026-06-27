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
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	result := Attempt{Number: attempt, CloneName: name}
	if pod, err := c.GetTypedPod(ctx, namespace, name); err == nil {
		updateAttemptFromPod(&result, pod)
		if pod.Status.Phase == corev1.PodSucceeded {
			if allowCompletion {
				result.Ready = true
				result.Message = "shadow batch pod completed successfully"
				return result
			}
			result.Message = "shadow pod completed before becoming ready"
			enrichFailure(c, namespace, name, &result)
			return result
		}
		if terminalFailure(result.ExitReason) || pod.Status.Phase == corev1.PodFailed {
			enrichFailure(c, namespace, name, &result)
			return result
		}
		if pod.Status.Phase == corev1.PodRunning && podReady(pod) {
			result.Ready = true
			result.Message = "shadow pod is running and ready"
			return result
		}
	}
	watcher, err := c.WatchPod(ctx, namespace, name)
	if err != nil {
		result.Message = err.Error()
		return result
	}
	defer watcher.Stop()

	for {
		select {
		case <-ctx.Done():
			result.Message = "verification timed out"
			enrichFailure(c, namespace, name, &result)
			return result
		case event, ok := <-watcher.ResultChan():
			if !ok {
				result.Message = "pod watch ended before verification completed"
				enrichFailure(c, namespace, name, &result)
				return result
			}
			pod, ok := event.Object.(*corev1.Pod)
			if !ok || pod == nil {
				continue
			}
			updateAttemptFromPod(&result, pod)
			if pod.Status.Phase == corev1.PodSucceeded {
				if allowCompletion {
					result.Ready = true
					result.Message = "shadow batch pod completed successfully"
					return result
				}
				result.Message = "shadow pod completed before becoming ready"
				enrichFailure(c, namespace, name, &result)
				return result
			}
			if terminalFailure(result.ExitReason) {
				enrichFailure(c, namespace, name, &result)
				return result
			}
			if pod.Status.Phase == corev1.PodRunning && podReady(pod) {
				result.Ready = true
				result.Message = "shadow pod is running and ready"
				return result
			}
			if pod.Status.Phase == corev1.PodFailed {
				enrichFailure(c, namespace, name, &result)
				return result
			}
		}
	}
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
	hasReadyCondition := false
	for _, cond := range pod.Status.Conditions {
		if cond.Type == corev1.PodReady {
			hasReadyCondition = true
			return cond.Status == corev1.ConditionTrue
		}
	}
	if hasReadyCondition {
		return false
	}
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
}
