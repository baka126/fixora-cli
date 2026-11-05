package shadow

import (
	"fmt"
	"math"
	"sort"
	"time"

	"k8s.io/apimachinery/pkg/api/resource"
)

const (
	oomHeadroomFloorPercent = 20.0
	oomGrowthLimitPercent   = 2.0
	oomMinimumSamples       = 3
	oomMinimumSpan          = 30 * time.Second
)

type memoryPoint struct {
	time  time.Time
	bytes int64
}

func assessOOMMemory(evidence *FailureEvidence, failingContainer string) MemoryAssessment {
	inconclusive := func(summary string) MemoryAssessment {
		return MemoryAssessment{Status: "inconclusive", Summary: summary}
	}
	if evidence == nil || failingContainer == "" {
		return inconclusive("the failing container could not be identified")
	}
	var container *ContainerEvidence
	for i := range evidence.Containers {
		if evidence.Containers[i].Name != failingContainer {
			continue
		}
		if container != nil {
			return inconclusive("the failing container identity is ambiguous")
		}
		container = &evidence.Containers[i]
	}
	if container == nil {
		return inconclusive("the failing container was not observed in the candidate")
	}
	if container.TerminationReason == "OOMKilled" {
		return MemoryAssessment{Status: "failed", Summary: "the candidate container was OOMKilled during shadow verification"}
	}
	limit, err := resource.ParseQuantity(container.MemoryLimit)
	if err != nil || limit.Sign() <= 0 {
		return inconclusive("the candidate has no usable memory limit for headroom assessment")
	}
	limitBytes := limit.Value()
	if limitBytes <= 0 {
		return inconclusive("the candidate has no usable memory limit for headroom assessment")
	}

	// Metrics-server may return the same sample more than once. Collapse each
	// timestamp to its highest observed usage so duplicates cannot inflate the
	// sample count or hide a peak.
	byTimestamp := make(map[time.Time]int64)
	for _, sample := range evidence.MemoryCurve {
		if sample.Timestamp.IsZero() {
			continue
		}
		for _, usage := range sample.Containers {
			if usage.Name != failingContainer {
				continue
			}
			bytes, ok := memorySampleBytes(usage)
			if !ok {
				continue
			}
			if current, exists := byTimestamp[sample.Timestamp]; !exists || bytes > current {
				byTimestamp[sample.Timestamp] = bytes
			}
		}
	}
	if len(byTimestamp) < oomMinimumSamples {
		return inconclusive("at least three distinct memory samples are required")
	}
	points := make([]memoryPoint, 0, len(byTimestamp))
	for timestamp, bytes := range byTimestamp {
		points = append(points, memoryPoint{time: timestamp, bytes: bytes})
	}
	sort.Slice(points, func(i, j int) bool { return points[i].time.Before(points[j].time) })
	span := points[len(points)-1].time.Sub(points[0].time)
	if span < oomMinimumSpan {
		return inconclusive("memory samples span less than 30 seconds")
	}
	peak := int64(0)
	for _, point := range points {
		if point.bytes > peak {
			peak = point.bytes
		}
	}
	headroom := (1 - float64(peak)/float64(limitBytes)) * 100
	assessment := MemoryAssessment{
		Peak: resource.NewQuantity(peak, resource.BinarySI).String(), PeakBytes: peak,
		Limit: limit.String(), LimitBytes: limitBytes, HeadroomPercent: math.Round(headroom*100) / 100,
		SampleCount: len(points), ObservationSeconds: int64(span.Seconds()),
	}
	if headroom+1e-9 < oomHeadroomFloorPercent {
		assessment.Status = "failed"
		assessment.Trend = "not-assessed"
		assessment.Summary = fmt.Sprintf("peak memory used %.1f%% of the %s limit; at least %.0f%% headroom is required", float64(peak)/float64(limitBytes)*100, limit.String(), oomHeadroomFloorPercent)
		return assessment
	}
	growthPercentPerMinute := memoryGrowthPercentPerMinute(points, limitBytes)
	if growthPercentPerMinute > oomGrowthLimitPercent {
		assessment.Status = "failed"
		assessment.Trend = "climbing"
		assessment.Summary = fmt.Sprintf("memory usage is climbing by %.2f%% of the limit per minute", growthPercentPerMinute)
		return assessment
	}
	assessment.Status = "passed"
	assessment.Trend = "flat"
	assessment.Summary = fmt.Sprintf("peak %s of %s; %.1f%% headroom with a flat observed trend", assessment.Peak, assessment.Limit, assessment.HeadroomPercent)
	return assessment
}

func memorySampleBytes(sample ContainerMemorySample) (int64, bool) {
	if sample.MemoryBytes >= 0 && (sample.MemoryBytes > 0 || sample.Memory == "" || sample.Memory == "0") {
		return sample.MemoryBytes, true
	}
	quantity, err := resource.ParseQuantity(sample.Memory)
	if err != nil || quantity.Sign() < 0 {
		return 0, false
	}
	return quantity.Value(), true
}

func memoryGrowthPercentPerMinute(points []memoryPoint, limitBytes int64) float64 {
	if len(points) < 2 || limitBytes <= 0 {
		return 0
	}
	origin := points[0].time
	var meanMinutes, meanBytes float64
	for _, point := range points {
		meanMinutes += point.time.Sub(origin).Minutes()
		meanBytes += float64(point.bytes)
	}
	meanMinutes /= float64(len(points))
	meanBytes /= float64(len(points))
	var covariance, variance float64
	for _, point := range points {
		deltaMinutes := point.time.Sub(origin).Minutes() - meanMinutes
		covariance += deltaMinutes * (float64(point.bytes) - meanBytes)
		variance += deltaMinutes * deltaMinutes
	}
	if variance == 0 {
		return 0
	}
	slopeBytesPerMinute := covariance / variance
	return slopeBytesPerMinute / float64(limitBytes) * 100
}
