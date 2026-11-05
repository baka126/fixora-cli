package shadow

import (
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
)

func readyPod(restarts int32) *corev1.Pod {
	return &corev1.Pod{
		Status: corev1.PodStatus{
			Phase:      corev1.PodRunning,
			Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}},
			ContainerStatuses: []corev1.ContainerStatus{
				{Name: "app", Ready: true, RestartCount: restarts},
			},
		},
	}
}

func TestSoakWindowCapsAtThirtySeconds(t *testing.T) {
	if got := soakWindow(10*time.Minute, false); got != 30*time.Second {
		t.Fatalf("want 30s, got %s", got)
	}
}

func TestSoakWindowIsQuarterOfShortTimeout(t *testing.T) {
	if got := soakWindow(60*time.Second, false); got != 15*time.Second {
		t.Fatalf("want 15s, got %s", got)
	}
}

func TestSoakWindowZeroWhenCompletionAllowed(t *testing.T) {
	// A Job clone legitimately reaches Succeeded and must not be failed for
	// leaving Running, so batch workloads skip the soak entirely.
	if got := soakWindow(10*time.Minute, true); got != 0 {
		t.Fatalf("want 0, got %s", got)
	}
}

func TestStillStableAcceptsSteadyPod(t *testing.T) {
	if reason, ok := stillStable(readyPod(0), 0); !ok {
		t.Fatalf("steady pod should stay stable, got %q", reason)
	}
}

func TestStillStableRejectsRestartDuringWindow(t *testing.T) {
	reason, ok := stillStable(readyPod(1), 0)
	if ok {
		t.Fatal("a restart during the soak window must break the streak")
	}
	if reason == "" {
		t.Fatal("want a reason explaining the restart")
	}
}

func TestStillStableRejectsReadyGoingFalse(t *testing.T) {
	pod := readyPod(0)
	pod.Status.Conditions[0].Status = corev1.ConditionFalse
	if _, ok := stillStable(pod, 0); ok {
		t.Fatal("pod that stops being ready must break the streak")
	}
}

func TestStillStableRejectsLeavingRunning(t *testing.T) {
	pod := readyPod(0)
	pod.Status.Phase = corev1.PodFailed
	if _, ok := stillStable(pod, 0); ok {
		t.Fatal("pod leaving Running must break the streak")
	}
}

func TestStillStableRejectsTerminalReason(t *testing.T) {
	pod := readyPod(0)
	pod.Status.ContainerStatuses[0].State = corev1.ContainerState{
		Terminated: &corev1.ContainerStateTerminated{Reason: "OOMKilled"},
	}
	if _, ok := stillStable(pod, 0); ok {
		t.Fatal("terminal exit reason must break the streak")
	}
}
