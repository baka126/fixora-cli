package termui

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/fixora/kubectl-fixora/internal/shadow"
)

func TestShadowProgressPlainEmitsReadableLinesWithoutANSI(t *testing.T) {
	var out bytes.Buffer
	progress := NewShadowProgress(&out, ShadowProgressOptions{Plain: true})
	progress.Update(shadow.ProgressEvent{Attempt: 2, Phase: shadow.ProgressStartup, Elapsed: 4 * time.Second, PhaseLimit: time.Minute})
	progress.Update(shadow.ProgressEvent{Attempt: 2, Phase: shadow.ProgressSoak, Ready: true, PhaseElapsed: 30 * time.Second, PhaseLimit: 2 * time.Minute, Restarts: 0})
	progress.Update(shadow.ProgressEvent{Attempt: 2, Phase: shadow.ProgressEvidence, Ready: true, EvidenceSamples: 3, PhaseElapsed: 35 * time.Second, PhaseLimit: 2 * time.Minute})
	progress.Update(shadow.ProgressEvent{Attempt: 2, Phase: shadow.ProgressVerified, Ready: true, PhaseElapsed: 2 * time.Minute, PhaseLimit: 2 * time.Minute})
	progress.Finish()

	got := out.String()
	for _, want := range []string{"attempt 2", "startup", "soak", "evidence samples: 3", "verified"} {
		if !strings.Contains(strings.ToLower(got), want) {
			t.Fatalf("plain progress omitted %q: %q", want, got)
		}
	}
	if strings.Contains(got, "\x1b") || strings.Contains(got, "\r") {
		t.Fatalf("plain output contains terminal control characters: %q", got)
	}
}

func TestShadowProgressTerminalUpdatesOneLineUntilFinished(t *testing.T) {
	var out bytes.Buffer
	progress := NewShadowProgress(&out, ShadowProgressOptions{Terminal: true})
	progress.Update(shadow.ProgressEvent{Attempt: 1, Phase: shadow.ProgressStartup})
	progress.Update(shadow.ProgressEvent{Attempt: 1, Phase: shadow.ProgressSoak, Ready: true, PhaseLimit: 2 * time.Minute})
	if strings.Contains(out.String(), "\n") {
		t.Fatalf("terminal updates should stay on one line until finish: %q", out.String())
	}
	if !strings.Contains(out.String(), "\r\x1b[2K") {
		t.Fatalf("terminal progress did not redraw in place: %q", out.String())
	}
	progress.Finish()
	if !strings.HasSuffix(out.String(), "\n") {
		t.Fatalf("finish should terminate the progress line: %q", out.String())
	}
}

func TestShadowProgressNonTerminalPrintsOnlyPhaseTransitions(t *testing.T) {
	var out bytes.Buffer
	progress := NewShadowProgress(&out, ShadowProgressOptions{})
	progress.Update(shadow.ProgressEvent{Attempt: 1, Phase: shadow.ProgressStartup})
	progress.Update(shadow.ProgressEvent{Attempt: 1, Phase: shadow.ProgressStartup, Elapsed: 5 * time.Second})
	progress.Update(shadow.ProgressEvent{Attempt: 1, Phase: shadow.ProgressSoak, Ready: true})
	progress.Update(shadow.ProgressEvent{Attempt: 1, Phase: shadow.ProgressSoak, PhaseElapsed: 5 * time.Second})
	progress.Update(shadow.ProgressEvent{Attempt: 1, Phase: shadow.ProgressVerified, Ready: true})
	progress.Finish()

	if got := strings.Count(out.String(), "\n"); got != 3 {
		t.Fatalf("non-terminal output should emit startup, soak, and verdict only; got %d lines: %q", got, out.String())
	}
}
