package shadow

import "testing"

func resourcesPatch(name string, requests, limits map[string]any) map[string]any {
	res := map[string]any{}
	if requests != nil {
		res["requests"] = requests
	}
	if limits != nil {
		res["limits"] = limits
	}
	return map[string]any{"containers": []any{map[string]any{"name": name, "resources": res}}}
}

func TestResourceCeilingUnderLimitPasses(t *testing.T) {
	rev := resourcesPatch("app", map[string]any{"memory": "8Gi", "cpu": "2"}, nil)
	if reasons := validateResourceCeiling(rev, DefaultPatchPolicy()); len(reasons) != 0 {
		t.Fatalf("expected no reasons, got %v", reasons)
	}
}

func TestResourceCeilingMemoryOverLimitRejected(t *testing.T) {
	rev := resourcesPatch("app", map[string]any{"memory": "900Gi"}, nil)
	if reasons := validateResourceCeiling(rev, DefaultPatchPolicy()); len(reasons) == 0 {
		t.Fatal("expected memory ceiling rejection")
	}
}

func TestResourceCeilingCPUOverLimitRejected(t *testing.T) {
	rev := resourcesPatch("app", nil, map[string]any{"cpu": "64"})
	if reasons := validateResourceCeiling(rev, DefaultPatchPolicy()); len(reasons) == 0 {
		t.Fatal("expected cpu ceiling rejection")
	}
}

func TestResourceCeilingNumericCPUParsed(t *testing.T) {
	// cpu as a bare YAML number (float64) must still be parsed, not skipped.
	rev := resourcesPatch("app", map[string]any{"cpu": float64(64)}, nil)
	if reasons := validateResourceCeiling(rev, DefaultPatchPolicy()); len(reasons) == 0 {
		t.Fatal("numeric cpu over the ceiling must be rejected")
	}
}

func TestResourceCeilingUnparseableRejected(t *testing.T) {
	rev := resourcesPatch("app", map[string]any{"memory": "lots"}, nil)
	if reasons := validateResourceCeiling(rev, DefaultPatchPolicy()); len(reasons) == 0 {
		t.Fatal("expected rejection for an unparseable quantity")
	}
}

func TestResourceCeilingUnlimitedWhenZero(t *testing.T) {
	rev := resourcesPatch("app", map[string]any{"memory": "900Gi", "cpu": "999"}, nil)
	policy := PatchPolicy{MaxMemoryBytes: 0, MaxCPUMillicores: 0}
	if reasons := validateResourceCeiling(rev, policy); len(reasons) != 0 {
		t.Fatalf("zero ceiling means unlimited, got %v", reasons)
	}
}
