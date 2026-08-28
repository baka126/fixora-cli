package analyzer

import (
	"strings"
	"testing"
)

// jobFixture builds a minimal Job map[string]any for testing.
func jobFixture(namespace, name string, failed, succeeded, backoffLimit int) map[string]any {
	spec := map[string]any{}
	if backoffLimit >= 0 {
		spec["backoffLimit"] = float64(backoffLimit)
	}
	status := map[string]any{}
	if failed > 0 {
		status["failed"] = float64(failed)
	}
	if succeeded > 0 {
		status["succeeded"] = float64(succeeded)
	}
	return map[string]any{
		"metadata": map[string]any{"namespace": namespace, "name": name},
		"spec":     spec,
		"status":   status,
	}
}

// cronJobFixture builds a minimal CronJob map[string]any for testing.
func cronJobFixture(namespace, name, concurrencyPolicy string, activeCount int) map[string]any {
	spec := map[string]any{}
	if concurrencyPolicy != "" {
		spec["concurrencyPolicy"] = concurrencyPolicy
	}
	var active []any
	for i := 0; i < activeCount; i++ {
		active = append(active, map[string]any{"name": "run"})
	}
	status := map[string]any{"active": active}
	return map[string]any{
		"metadata": map[string]any{"namespace": namespace, "name": name},
		"spec":     spec,
		"status":   status,
	}
}

// --- JobRetrying tests ---

func TestJobRetryingEmittedWhenBelowBackoffLimit(t *testing.T) {
	ctx := scanContextWithItems(map[string][]map[string]any{
		// failed=2, backoffLimit=6, succeeded=0 → JobRetrying
		"jobs": {jobFixture("prod", "batch", 2, 0, 6)},
	})
	findings, err := New(fakeReader{}, Options{Namespace: "prod"}).analyzeJobs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	assertFindingStatus(t, findings, "JobRetrying")
	// Must NOT also emit Failed (still below limit).
	for _, f := range findings {
		if f.ResourceName == "batch" && f.Status == "Failed" {
			t.Fatalf("expected no Failed finding when below backoffLimit, got %#v", f)
		}
	}
}

func TestJobRetryingEvidenceContainsFailedAndLimit(t *testing.T) {
	ctx := scanContextWithItems(map[string][]map[string]any{
		"jobs": {jobFixture("prod", "batch", 2, 0, 6)},
	})
	findings, err := New(fakeReader{}, Options{Namespace: "prod"}).analyzeJobs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var retrying *Finding
	for i := range findings {
		if findings[i].Status == "JobRetrying" {
			retrying = &findings[i]
		}
	}
	if retrying == nil {
		t.Fatal("expected JobRetrying finding")
	}
	labels := map[string]bool{}
	for _, e := range retrying.Evidence {
		labels[e.Label] = true
	}
	if !labels["Failed"] {
		t.Fatalf("expected 'Failed' evidence label, got %#v", retrying.Evidence)
	}
	if !labels["BackoffLimit"] {
		t.Fatalf("expected 'BackoffLimit' evidence label, got %#v", retrying.Evidence)
	}
}

func TestJobAtBackoffLimitEmitsFailedNotRetrying(t *testing.T) {
	ctx := scanContextWithItems(map[string][]map[string]any{
		// failed == backoffLimit → Failed (high), no JobRetrying
		"jobs": {jobFixture("prod", "batch", 6, 0, 6)},
	})
	findings, err := New(fakeReader{}, Options{Namespace: "prod"}).analyzeJobs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	assertFindingStatus(t, findings, "Failed")
	for _, f := range findings {
		if f.Status == "JobRetrying" {
			t.Fatalf("expected no JobRetrying when failed >= backoffLimit, got %#v", f)
		}
	}
}

func TestJobOverBackoffLimitEmitsFailedNotRetrying(t *testing.T) {
	ctx := scanContextWithItems(map[string][]map[string]any{
		// failed > backoffLimit → Failed (high), no JobRetrying
		"jobs": {jobFixture("prod", "batch", 8, 0, 6)},
	})
	findings, err := New(fakeReader{}, Options{Namespace: "prod"}).analyzeJobs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	assertFindingStatus(t, findings, "Failed")
	for _, f := range findings {
		if f.Status == "JobRetrying" {
			t.Fatalf("expected no JobRetrying when failed > backoffLimit, got %#v", f)
		}
	}
}

func TestJobWithSucceededDoesNotEmitRetrying(t *testing.T) {
	ctx := scanContextWithItems(map[string][]map[string]any{
		// succeeded > 0 means job completed — no retrying finding
		"jobs": {jobFixture("prod", "batch", 2, 1, 6)},
	})
	findings, err := New(fakeReader{}, Options{Namespace: "prod"}).analyzeJobs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range findings {
		if f.Status == "JobRetrying" {
			t.Fatalf("expected no JobRetrying when succeeded>0, got %#v", f)
		}
	}
}

// --- CronJobOverlap tests ---

func TestCronJobOverlapEmittedWhenAllowAndActiveGTOne(t *testing.T) {
	ctx := scanContextWithItems(map[string][]map[string]any{
		// concurrencyPolicy=Allow, 2 active runs → CronJobOverlap
		"cronjobs": {cronJobFixture("prod", "nightly", "Allow", 2)},
	})
	findings, err := New(fakeReader{}, Options{Namespace: "prod"}).analyzeCronJobs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	assertFindingStatus(t, findings, "CronJobOverlap")
}

func TestCronJobOverlapEmptyPolicyDefaultsToAllow(t *testing.T) {
	ctx := scanContextWithItems(map[string][]map[string]any{
		// concurrencyPolicy="" → treated as "Allow"
		"cronjobs": {cronJobFixture("prod", "nightly", "", 2)},
	})
	findings, err := New(fakeReader{}, Options{Namespace: "prod"}).analyzeCronJobs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	assertFindingStatus(t, findings, "CronJobOverlap")
}

func TestCronJobOverlapEvidenceContainsActiveAndPolicy(t *testing.T) {
	ctx := scanContextWithItems(map[string][]map[string]any{
		"cronjobs": {cronJobFixture("prod", "nightly", "Allow", 3)},
	})
	findings, err := New(fakeReader{}, Options{Namespace: "prod"}).analyzeCronJobs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var overlap *Finding
	for i := range findings {
		if findings[i].Status == "CronJobOverlap" {
			overlap = &findings[i]
		}
	}
	if overlap == nil {
		t.Fatal("expected CronJobOverlap finding")
	}
	labels := map[string]bool{}
	for _, e := range overlap.Evidence {
		labels[e.Label] = true
	}
	if !labels["Active Runs"] {
		t.Fatalf("expected 'Active Runs' evidence label, got %#v", overlap.Evidence)
	}
	if !labels["ConcurrencyPolicy"] {
		t.Fatalf("expected 'ConcurrencyPolicy' evidence label, got %#v", overlap.Evidence)
	}
}

func TestCronJobForbidWithMultipleActiveNoOverlapFinding(t *testing.T) {
	ctx := scanContextWithItems(map[string][]map[string]any{
		// policy=Forbid — no overlap finding even if active > 1
		"cronjobs": {cronJobFixture("prod", "nightly", "Forbid", 2)},
	})
	findings, err := New(fakeReader{}, Options{Namespace: "prod"}).analyzeCronJobs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range findings {
		if f.Status == "CronJobOverlap" {
			t.Fatalf("expected no CronJobOverlap when policy=Forbid, got %#v", f)
		}
	}
}

func TestCronJobReplaceWithMultipleActiveNoOverlapFinding(t *testing.T) {
	ctx := scanContextWithItems(map[string][]map[string]any{
		// policy=Replace — no overlap finding
		"cronjobs": {cronJobFixture("prod", "nightly", "Replace", 2)},
	})
	findings, err := New(fakeReader{}, Options{Namespace: "prod"}).analyzeCronJobs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range findings {
		if f.Status == "CronJobOverlap" {
			t.Fatalf("expected no CronJobOverlap when policy=Replace, got %#v", f)
		}
	}
}

func TestCronJobAllowSingleActiveNoOverlapFinding(t *testing.T) {
	ctx := scanContextWithItems(map[string][]map[string]any{
		// policy=Allow but only 1 active → no overlap
		"cronjobs": {cronJobFixture("prod", "nightly", "Allow", 1)},
	})
	findings, err := New(fakeReader{}, Options{Namespace: "prod"}).analyzeCronJobs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range findings {
		if f.Status == "CronJobOverlap" {
			t.Fatalf("expected no CronJobOverlap with only 1 active run, got %#v", f)
		}
	}
}

// --- Collision guard: new statuses must not match any BuildPlan switch case ---
//
// The switch arms in fix.BuildPlan use strings.Contains on finding.Status. We
// verify here that neither new status is a substring of any known arm keyword,
// which would cause unintended strategy routing. The full round-trip collision
// test lives in internal/fix/planner_test.go (TestTier3StatusesDefaultToBuildPlan).

func TestJobRetryingStatusIsNotSubstringOfBuildPlanArms(t *testing.T) {
	// Known BuildPlan arm keywords (from fix.BuildPlan switch, which uses strings.Contains):
	arms := []string{
		"ImagePull", "OOMKilled", "CrashLoopBackOff", "CreateContainerConfigError",
		"ExecFormatError", "PermissionDenied", "NoEndpoints", "ConnectionRefused",
		"HPA", "Evicted", "Webhook",
	}
	status := "JobRetrying"
	for _, arm := range arms {
		if strings.Contains(arm, status) || strings.Contains(status, arm) {
			t.Fatalf("JobRetrying overlaps with BuildPlan arm %q", arm)
		}
	}
}

func TestCronJobOverlapStatusIsNotSubstringOfBuildPlanArms(t *testing.T) {
	arms := []string{
		"ImagePull", "OOMKilled", "CrashLoopBackOff", "CreateContainerConfigError",
		"ExecFormatError", "PermissionDenied", "NoEndpoints", "ConnectionRefused",
		"HPA", "Evicted", "Webhook",
	}
	status := "CronJobOverlap"
	for _, arm := range arms {
		if strings.Contains(arm, status) || strings.Contains(status, arm) {
			t.Fatalf("CronJobOverlap overlaps with BuildPlan arm %q", arm)
		}
	}
}
