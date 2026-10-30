package shadow

import (
	"testing"
	"time"
)

func memoryEvidence(samples ...int64) *FailureEvidence {
	start := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	evidence := &FailureEvidence{Containers: []ContainerEvidence{{Name: "api", MemoryLimit: "100Mi"}}}
	for i, bytes := range samples {
		evidence.MemoryCurve = append(evidence.MemoryCurve, MemorySample{
			Timestamp:  start.Add(time.Duration(i*15) * time.Second),
			Containers: []ContainerMemorySample{{Name: "api", Memory: "", MemoryBytes: bytes}},
		})
	}
	return evidence
}

func TestOOMMemoryAssessmentPassesAtExactlyTwentyPercentHeadroom(t *testing.T) {
	assessment := assessOOMMemory(memoryEvidence(80*1024*1024, 80*1024*1024, 80*1024*1024), "api")
	if assessment.Status != "passed" || assessment.HeadroomPercent != 20 || assessment.Trend != "flat" {
		t.Fatalf("want exactly 20%% headroom with a flat curve to pass, got %#v", assessment)
	}
}

func TestOOMMemoryAssessmentFailsBelowTwentyPercentHeadroom(t *testing.T) {
	assessment := assessOOMMemory(memoryEvidence(81*1024*1024, 81*1024*1024, 81*1024*1024), "api")
	if assessment.Status != "failed" || assessment.HeadroomPercent >= 20 {
		t.Fatalf("want less than 20%% headroom to fail, got %#v", assessment)
	}
}

func TestOOMMemoryAssessmentFailsClimbingCurve(t *testing.T) {
	assessment := assessOOMMemory(memoryEvidence(50*1024*1024, 55*1024*1024, 60*1024*1024), "api")
	if assessment.Status != "failed" || assessment.Trend != "climbing" {
		t.Fatalf("want climbing memory trend to fail, got %#v", assessment)
	}
}

func TestOOMMemoryAssessmentPassesAtGrowthThreshold(t *testing.T) {
	assessment := assessOOMMemory(memoryEvidence(50*1024*1024, 50*1024*1024+512*1024, 51*1024*1024), "api")
	if assessment.Status != "passed" || assessment.Trend != "flat" {
		t.Fatalf("growth at exactly 2%% of limit per minute should pass, got %#v", assessment)
	}
}

func TestOOMMemoryAssessmentSortsOutOfOrderTimestamps(t *testing.T) {
	evidence := memoryEvidence(70*1024*1024, 70*1024*1024, 70*1024*1024)
	evidence.MemoryCurve[0], evidence.MemoryCurve[2] = evidence.MemoryCurve[2], evidence.MemoryCurve[0]
	assessment := assessOOMMemory(evidence, "api")
	if assessment.Status != "passed" || assessment.SampleCount != 3 || assessment.ObservationSeconds != 30 {
		t.Fatalf("out-of-order samples should be sorted by timestamp, got %#v", assessment)
	}
}

func TestOOMMemoryAssessmentIsInconclusiveWithoutReliableEvidence(t *testing.T) {
	tests := []struct {
		name      string
		evidence  *FailureEvidence
		container string
	}{
		{name: "missing container", evidence: memoryEvidence(40, 40, 40), container: "worker"},
		{name: "missing limit", evidence: &FailureEvidence{Containers: []ContainerEvidence{{Name: "api"}}}, container: "api"},
		{name: "malformed limit", evidence: withMemoryLimit(memoryEvidence(40, 40, 40), "not-a-quantity"), container: "api"},
		{name: "fewer than three unique timestamps", evidence: memoryEvidence(40, 40), container: "api"},
		{name: "less than thirty seconds", evidence: shortSpanMemoryEvidence(), container: "api"},
		{name: "duplicate timestamps", evidence: duplicateTimestampMemoryEvidence(), container: "api"},
		{name: "malformed sample", evidence: malformedMemorySampleEvidence(), container: "api"},
		{name: "ambiguous container", evidence: duplicateContainerMemoryEvidence(), container: "api"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assessment := assessOOMMemory(test.evidence, test.container)
			if assessment.Status != "inconclusive" {
				t.Fatalf("want missing or inadequate evidence to be inconclusive, got %#v", assessment)
			}
		})
	}
}

func withMemoryLimit(evidence *FailureEvidence, limit string) *FailureEvidence {
	evidence.Containers[0].MemoryLimit = limit
	return evidence
}

func malformedMemorySampleEvidence() *FailureEvidence {
	evidence := memoryEvidence(40*1024*1024, 41*1024*1024, 42*1024*1024)
	evidence.MemoryCurve[0].Containers[0].MemoryBytes = -1
	evidence.MemoryCurve[0].Containers[0].Memory = "not-a-quantity"
	return evidence
}

func duplicateContainerMemoryEvidence() *FailureEvidence {
	evidence := memoryEvidence(40*1024*1024, 40*1024*1024, 40*1024*1024)
	evidence.Containers = append(evidence.Containers, evidence.Containers[0])
	return evidence
}

func shortSpanMemoryEvidence() *FailureEvidence {
	evidence := memoryEvidence(40*1024*1024, 40*1024*1024, 40*1024*1024)
	evidence.MemoryCurve[2].Timestamp = evidence.MemoryCurve[0].Timestamp.Add(25 * time.Second)
	return evidence
}

func duplicateTimestampMemoryEvidence() *FailureEvidence {
	evidence := memoryEvidence(40*1024*1024, 41*1024*1024, 42*1024*1024)
	evidence.MemoryCurve[2].Timestamp = evidence.MemoryCurve[1].Timestamp
	return evidence
}

func TestOOMMemoryAssessmentFailsCandidateOOMRegardlessOfSamples(t *testing.T) {
	evidence := memoryEvidence(20*1024*1024, 20*1024*1024, 20*1024*1024)
	evidence.Containers[0].TerminationReason = "OOMKilled"
	assessment := assessOOMMemory(evidence, "api")
	if assessment.Status != "failed" {
		t.Fatalf("candidate OOM must fail regardless of sampled curve, got %#v", assessment)
	}
}
