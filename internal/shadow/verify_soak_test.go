package shadow

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/fixora/kubectl-fixora/internal/kube"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	corefake "k8s.io/client-go/kubernetes/fake"
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

func TestStartupAndStabilityWindowsMeetThreeMinuteHorizon(t *testing.T) {
	if startupWindow != 60*time.Second {
		t.Fatalf("want 60s startup allowance, got %s", startupWindow)
	}
	if got := soakWindow(false); got != 120*time.Second {
		t.Fatalf("want 120s stability window, got %s", got)
	}
}

func TestSoakWindowDoesNotShrinkToFitAttemptTimeout(t *testing.T) {
	if got := soakWindow(false); got != 120*time.Second {
		t.Fatalf("want fixed 120s stability window, got %s", got)
	}
}

func TestSoakWindowZeroWhenCompletionAllowed(t *testing.T) {
	// A Job clone legitimately reaches Succeeded and must not be failed for
	// leaving Running, so batch workloads skip the soak entirely.
	if got := soakWindow(true); got != 0 {
		t.Fatalf("want 0, got %s", got)
	}
}

func TestVerificationTimeoutIsInconclusive(t *testing.T) {
	attempt := Attempt{Message: "verification timed out", Inconclusive: true}
	if got := statusForAttempt(attempt); got != VerificationInconclusive {
		t.Fatalf("want inconclusive timeout, got %s", got)
	}
}

func TestVerificationFailureRemainsFailure(t *testing.T) {
	if got := statusForAttempt(Attempt{Message: "shadow pod did not stay ready"}); got != VerificationFailed {
		t.Fatalf("want failed verdict, got %s", got)
	}
}

func TestInitialReadyWindowUsesPodTransitionTimestampAfterSlowRead(t *testing.T) {
	created := time.Now().Add(-90 * time.Second)
	pod := readyPod(0)
	pod.CreationTimestamp = metav1.NewTime(created)
	pod.Status.Conditions[0].LastTransitionTime = metav1.NewTime(created.Add(50 * time.Second))
	within, known := initialReadyWithinWindow(pod, time.Now(), time.Now().Add(-70*time.Second), 60*time.Second)
	if !within || !known {
		t.Fatalf("ready transition at 50s should pass after delayed read, got within=%t known=%t", within, known)
	}
	pod.Status.Conditions[0].LastTransitionTime = metav1.NewTime(created.Add(70 * time.Second))
	within, known = initialReadyWithinWindow(pod, time.Now(), time.Now().Add(-70*time.Second), 60*time.Second)
	if within || !known {
		t.Fatalf("ready transition at 70s should be a conclusive startup failure, got within=%t known=%t", within, known)
	}
	within, known = initialReadyWithinWindow(pod, time.Now(), time.Now().Add(-10*time.Second), 60*time.Second)
	if within || !known {
		t.Fatalf("creation-relative startup limit must reject a 70s transition even when verification began recently, got within=%t known=%t", within, known)
	}
}

func TestVerifyCloneTimeoutIsInconclusive(t *testing.T) {
	client := &kube.TypedClient{Clientset: corefake.NewSimpleClientset(&corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "candidate", Namespace: "test"},
		Status:     corev1.PodStatus{Phase: corev1.PodPending},
	})}
	attempt := verifyCloneWithWindows(context.Background(), client, "test", "candidate", 10*time.Millisecond, 1, false, time.Second, time.Second)
	if !attempt.Inconclusive || statusForAttempt(attempt) != VerificationInconclusive {
		t.Fatalf("deadline without a verdict must be inconclusive, got %#v", attempt)
	}
}

func TestVerifyCloneRejectsRestartBeforeReadiness(t *testing.T) {
	pod := readyPod(1)
	pod.Namespace = "test"
	pod.Name = "candidate"
	client := &kube.TypedClient{Clientset: corefake.NewSimpleClientset(pod)}
	attempt := verifyCloneWithWindows(context.Background(), client, "test", "candidate", time.Second, 1, false, time.Second, time.Second)
	if attempt.Ready || attempt.Inconclusive || !strings.Contains(attempt.Message, "restarted during startup") {
		t.Fatalf("startup restart must fail verification, got %#v", attempt)
	}
}

func TestVerifyCloneStartsStabilityWindowFromInitialReadyRead(t *testing.T) {
	pod := readyPod(0)
	pod.Namespace = "test"
	pod.Name = "candidate"
	client := &kube.TypedClient{Clientset: corefake.NewSimpleClientset(pod)}
	attempt := verifyCloneWithWindows(context.Background(), client, "test", "candidate", time.Second, 1, false, time.Second, 10*time.Millisecond)
	if !attempt.Ready {
		t.Fatalf("already-ready pod should complete its stability window, got %#v", attempt)
	}
}

func TestVerifyCloneDoesNotPassRunningBatchPod(t *testing.T) {
	pod := readyPod(0)
	pod.Namespace = "test"
	pod.Name = "batch"
	client := &kube.TypedClient{Clientset: corefake.NewSimpleClientset(pod)}
	attempt := verifyCloneWithWindows(context.Background(), client, "test", "batch", 10*time.Millisecond, 1, true, time.Second, 0)
	if attempt.Ready || !attempt.Inconclusive {
		t.Fatalf("a still-running batch pod must not pass before completion, got %#v", attempt)
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
