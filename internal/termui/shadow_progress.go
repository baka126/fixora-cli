package termui

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/fixora/kubectl-fixora/internal/shadow"
)

type ShadowProgressOptions struct {
	Plain    bool
	Terminal bool
	Detailed bool
	Target   string
	Failure  string
}

// ShadowProgress renders bounded verification state without logs, resource
// values, or configuration contents.
type ShadowProgress struct {
	out       io.Writer
	plain     bool
	terminal  bool
	lineOpen  bool
	lastPhase shadow.ProgressPhase
	detailed  bool
	target    string
	failure   string
}

func NewShadowProgress(out io.Writer, opts ShadowProgressOptions) *ShadowProgress {
	return &ShadowProgress{out: out, plain: opts.Plain, terminal: opts.Terminal && !opts.Plain, detailed: opts.Detailed, target: opts.Target, failure: opts.Failure}
}

func (p *ShadowProgress) Update(event shadow.ProgressEvent) {
	if p == nil || p.out == nil {
		return
	}
	line := shadowProgressLine(event, p.failure)
	if p.terminal {
		if p.detailed {
			p.redrawLines(shadowProgressBlock(event, p.target, p.failure))
		} else {
			_, _ = fmt.Fprintf(p.out, "\r\x1b[2K%s", line)
		}
		p.lineOpen = true
		p.lastPhase = event.Phase
		return
	}
	if !p.plain && event.Phase == p.lastPhase {
		return
	}
	_, _ = fmt.Fprintln(p.out, line)
	p.lastPhase = event.Phase
}

func (p *ShadowProgress) redrawLines(lines []string) {
	if p.lineOpen && len(lines) > 1 {
		_, _ = fmt.Fprintf(p.out, "\r\x1b[%dA", len(lines)-1)
	}
	for i, line := range lines {
		_, _ = fmt.Fprintf(p.out, "\r\x1b[2K%s", line)
		if i < len(lines)-1 {
			_, _ = fmt.Fprint(p.out, "\n")
		}
	}
}

func (p *ShadowProgress) Finish() {
	if p == nil || !p.terminal || !p.lineOpen {
		return
	}
	_, _ = fmt.Fprintln(p.out)
	p.lineOpen = false
}

func shadowProgressLine(event shadow.ProgressEvent, failure string) string {
	parts := []string{fmt.Sprintf("Attempt %d", event.Attempt)}
	switch event.Phase {
	case shadow.ProgressStartup:
		startup := fmt.Sprintf("startup · waiting Ready %s/%s", progressDuration(event.Elapsed), progressDuration(event.PhaseLimit))
		if failure != "" {
			startup += " (was " + trim(failure, 16) + ")"
		}
		parts = append(parts, startup)
	case shadow.ProgressSoak:
		ready := "○"
		if event.Ready {
			ready = "✓"
		}
		soak := fmt.Sprintf("soak · Ready %s %s/%s · restarts %d", ready, progressDuration(event.PhaseElapsed), progressDuration(event.PhaseLimit), event.Restarts)
		if event.ReadyTimeKnown {
			soak += fmt.Sprintf(" · ready in %ds", event.ReadySeconds)
		}
		parts = append(parts, soak)
	case shadow.ProgressEvidence:
		parts = append(parts, fmt.Sprintf("evidence samples: %d", event.EvidenceSamples))
	case shadow.ProgressVerified:
		if event.EvidenceModule == nil {
			parts = append(parts, "pod stability verified · checking failure evidence")
		} else {
			parts = append(parts, "verified · "+trim(event.EvidenceModule.Module, 18)+" "+string(event.EvidenceModule.Status))
		}
	case shadow.ProgressInconclusive:
		parts = append(parts, "inconclusive · observation incomplete")
	default:
		parts = append(parts, "failed")
		if event.ExitReason != "" {
			parts = append(parts, trim(event.ExitReason, 24))
		}
	}
	if event.Phase == shadow.ProgressFailed || event.Phase == shadow.ProgressInconclusive {
		if event.Restarts > 0 {
			parts = append(parts, fmt.Sprintf("restarts %d", event.Restarts))
		}
	}
	if len(event.MemorySamples) > 0 {
		parts = append(parts, formatMemoryProgress(event))
	}
	if event.EvidenceModule != nil && event.EvidenceModule.Summary != "" {
		parts = append(parts, trim(event.EvidenceModule.Summary, 36))
	}
	return trim(strings.Join(parts, " · "), 78)
}

func shadowProgressBlock(event shadow.ProgressEvent, target, failure string) []string {
	phase := event.Phase
	startup := "○ waiting for Ready"
	if event.ReadyTimeKnown {
		startup = fmt.Sprintf("✓ Ready in %ds", event.ReadySeconds)
	} else if event.ReadyObserved {
		startup = "✓ Ready (time not observed)"
	} else if phase == shadow.ProgressFailed {
		startup = "✗ did not become Ready"
	} else if phase == shadow.ProgressInconclusive {
		startup = "? Ready time could not be confirmed"
	}
	if failure != "" {
		startup += " (original " + trim(failure, 18) + ")"
	}

	health := "○ waiting for readiness"
	if event.StabilityPassed {
		health = fmt.Sprintf("✓ Ready throughout %s", progressDuration(event.PhaseLimit))
	} else if phase == shadow.ProgressSoak || phase == shadow.ProgressEvidence || phase == shadow.ProgressVerified {
		health = fmt.Sprintf("✓ Ready for %s / %s", progressDuration(event.PhaseElapsed), progressDuration(event.PhaseLimit))
	} else if phase == shadow.ProgressFailed {
		health = "✗ candidate did not remain Ready"
	} else if phase == shadow.ProgressInconclusive {
		health = "? stability could not be confirmed"
	}

	restarts := fmt.Sprintf("✓ %d", event.Restarts)
	if event.Restarts > 0 || (phase == shadow.ProgressFailed && event.ExitReason != "") {
		restarts = "✗ " + fmt.Sprint(event.Restarts)
	}
	if event.ExitReason != "" {
		restarts += " · " + trim(event.ExitReason, 24)
	}

	evidence := "collecting"
	if event.EvidenceModule != nil {
		evidence = string(event.EvidenceModule.Status)
		if event.EvidenceModule.Module != "" {
			evidence = event.EvidenceModule.Module + " · " + evidence
		}
		if event.EvidenceModule.Summary != "" {
			evidence += " · " + event.EvidenceModule.Summary
		}
	} else if len(event.MemorySamples) > 0 {
		evidence = "memory " + formatMemoryProgress(event)
	} else if event.EvidenceSamples > 0 {
		evidence = fmt.Sprintf("%d memory samples", event.EvidenceSamples)
	} else if phase == shadow.ProgressVerified {
		evidence = "pod stability passed; evidence gate pending"
	}

	verdict := "checking"
	switch phase {
	case shadow.ProgressVerified:
		if event.EvidenceModule == nil {
			verdict = "pod stable · checking failure-specific evidence"
		} else if event.EvidenceModule.Status == shadow.EvidenceModulePassed {
			verdict = "VERIFIED"
		} else if event.EvidenceModule.Status == shadow.EvidenceModuleInconclusive || event.EvidenceModule.Status == shadow.EvidenceModuleUnsupported {
			verdict = "INCONCLUSIVE"
		} else {
			verdict = "FAILED"
		}
	case shadow.ProgressFailed:
		verdict = "FAILED"
	case shadow.ProgressInconclusive:
		verdict = "INCONCLUSIVE"
	}

	lines := []string{fmt.Sprintf("Fixora shadow · attempt %d", event.Attempt)}
	if target != "" {
		lines[0] += " · " + trim(target, 28)
	}
	lines = append(lines,
		"Startup gate · "+startup,
		"Health soak  · "+health,
		"Restarts     · "+restarts,
		"Evidence     · "+evidence,
		"Verdict      · "+verdict,
	)
	for i := range lines {
		lines[i] = trim(lines[i], 78)
	}
	return lines
}

func formatMemoryProgress(event shadow.ProgressEvent) string {
	parts := []string{}
	if event.MemoryContainer != "" {
		parts = append(parts, trim(event.MemoryContainer, 12))
	}
	parts = append(parts, memorySparkline(event.MemorySamples))
	if event.MemoryCurrent > 0 || event.MemoryPeak > 0 {
		parts = append(parts, humanBytes(event.MemoryCurrent)+" now / "+humanBytes(event.MemoryPeak)+" peak")
	}
	if event.MemoryHasLimit {
		parts = append(parts, humanBytes(event.MemoryLimit)+" limit")
		parts = append(parts, fmt.Sprintf("%.0f%% headroom", event.MemoryHeadroom))
	}
	if event.MemoryTrend != "" {
		parts = append(parts, "trend "+event.MemoryTrend)
	}
	return strings.Join(parts, " ")
}

func memorySparkline(values []int64) string {
	if len(values) == 0 {
		return ""
	}
	bars := []rune("▁▂▃▄▅▆▇█")
	minValue, maxValue := values[0], values[0]
	for _, value := range values[1:] {
		if value < minValue {
			minValue = value
		}
		if value > maxValue {
			maxValue = value
		}
	}
	var result strings.Builder
	result.Grow(len(values) * 3)
	for _, value := range values {
		level := 4
		if maxValue > minValue {
			level = int(float64(value-minValue) * 7 / float64(maxValue-minValue))
		}
		result.WriteRune(bars[level])
	}
	return result.String()
}

func humanBytes(value int64) string {
	if value < 1024 {
		return fmt.Sprintf("%dB", value)
	}
	units := []string{"Ki", "Mi", "Gi", "Ti"}
	amount := float64(value)
	unit := "B"
	for _, next := range units {
		amount /= 1024
		unit = next
		if amount < 1024 {
			break
		}
	}
	return fmt.Sprintf("%.0f%s", amount, unit)
}

func progressDuration(value time.Duration) string {
	seconds := int(value.Round(time.Second) / time.Second)
	return fmt.Sprintf("%d:%02d", seconds/60, seconds%60)
}
