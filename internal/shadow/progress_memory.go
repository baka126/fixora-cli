package shadow

import (
	"sort"
	"time"

	"k8s.io/apimachinery/pkg/api/resource"
)

func progressEventForAttempt(attempt int, phase ProgressPhase, elapsed, phaseElapsed, phaseLimit time.Duration, result Attempt, evidence *FailureEvidence, module *EvidenceModuleResult) ProgressEvent {
	event := ProgressEvent{
		Attempt: attempt, Phase: phase, Elapsed: elapsed, PhaseElapsed: phaseElapsed, PhaseLimit: phaseLimit,
		Ready: result.Ready, Restarts: result.Restarts, ExitReason: result.ExitReason,
		EvidenceSamples: 0, EvidenceModule: module,
	}
	event.StabilityPassed = phase == ProgressVerified && result.Ready && module == nil
	if evidence == nil {
		return event
	}
	event.ReadyObserved = evidence.ReadyAt != nil
	event.EvidenceSamples = len(evidence.MemoryCurve)
	if evidence.TimeToReadySeconds != nil {
		event.ReadySeconds, event.ReadyTimeKnown = *evidence.TimeToReadySeconds, true
	}
	if evidence.MemoryAssessment != nil {
		event.MemoryTrend = evidence.MemoryAssessment.Trend
	}
	container, samples := progressMemorySeries(evidence)
	if len(samples) == 0 {
		return event
	}
	event.MemoryContainer = container
	event.MemorySamples = samples
	event.MemoryCurrent = samples[len(samples)-1]
	for _, value := range samples {
		if value > event.MemoryPeak {
			event.MemoryPeak = value
		}
	}
	for _, item := range evidence.Containers {
		if item.Name != container || item.MemoryLimit == "" {
			continue
		}
		limit, err := resource.ParseQuantity(item.MemoryLimit)
		if err == nil && limit.Value() > 0 {
			event.MemoryLimit = limit.Value()
			event.MemoryHasLimit = true
			event.MemoryHeadroom = (1 - float64(event.MemoryPeak)/float64(event.MemoryLimit)) * 100
		}
		break
	}
	if assessment := evidence.MemoryAssessment; assessment != nil {
		if assessment.Trend != "" {
			event.MemoryTrend = assessment.Trend
		}
		if assessment.LimitBytes > 0 {
			event.MemoryLimit = assessment.LimitBytes
			event.MemoryHasLimit = true
			event.MemoryHeadroom = assessment.HeadroomPercent
		}
		if assessment.PeakBytes > 0 {
			event.MemoryPeak = assessment.PeakBytes
		}
	}
	return event
}

func progressMemorySeries(evidence *FailureEvidence) (string, []int64) {
	if evidence == nil {
		return "", nil
	}
	series := make(map[string][]int64)
	for _, sample := range evidence.MemoryCurve {
		for _, container := range sample.Containers {
			value, ok := memorySampleBytes(container)
			if ok {
				series[container.Name] = append(series[container.Name], value)
			}
		}
	}
	if len(series) == 0 {
		return "", nil
	}
	names := make([]string, 0, len(series))
	for name := range series {
		names = append(names, name)
	}
	sort.Strings(names)
	selected := names[0]
	for _, name := range names[1:] {
		if len(series[name]) > len(series[selected]) {
			selected = name
		}
	}
	values := series[selected]
	if len(values) > 16 {
		values = values[len(values)-16:]
	}
	return selected, append([]int64(nil), values...)
}
