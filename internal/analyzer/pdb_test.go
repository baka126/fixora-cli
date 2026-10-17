package analyzer

import (
	"context"
	"testing"
)

type pdbInventoryReader struct {
	fakeReader
	pdb map[string]any
}

func (r pdbInventoryReader) GetResource(_ context.Context, ns, res string) (map[string]any, error) {
	if res == "pdb/guard" {
		return r.pdb, nil
	}
	return map[string]any{"apiVersion": "apps/v1", "kind": "StatefulSet", "metadata": map[string]any{"name": "workers", "namespace": ns, "uid": "owner"}}, nil
}
func TestPDBInventoryIgnoresDisplayFilters(t *testing.T) {
	p := map[string]any{"apiVersion": "policy/v1", "kind": "PodDisruptionBudget", "metadata": map[string]any{"name": "guard", "namespace": "team", "uid": "pdb1", "resourceVersion": "1", "generation": 1, "labels": map[string]any{"view": "visible"}}, "spec": map[string]any{"selector": map[string]any{"matchLabels": map[string]any{"role": "api"}}}, "status": map[string]any{"observedGeneration": 1, "currentHealthy": 1, "desiredHealthy": 1, "expectedPods": 1, "disruptionsAllowed": 0}}
	other := map[string]any{"apiVersion": "policy/v1", "kind": "PodDisruptionBudget", "metadata": map[string]any{"name": "other", "namespace": "team", "uid": "pdb2"}, "spec": map[string]any{"selector": map[string]any{"matchLabels": map[string]any{"role": "api"}}}}
	pod := map[string]any{"metadata": map[string]any{"name": "one", "namespace": "team", "uid": "pod1", "labels": map[string]any{"role": "api"}, "ownerReferences": []any{map[string]any{"apiVersion": "apps/v1", "kind": "StatefulSet", "name": "workers", "uid": "owner", "controller": true}}}, "status": map[string]any{"phase": "Running", "conditions": []any{map[string]any{"type": "Ready", "status": "True"}}}}
	r := pdbInventoryReader{fakeReader: fakeReader{items: map[string][]map[string]any{"pdb": {p, other}, "pods": {pod}}}, pdb: p}
	opts := Options{Namespace: "team", LabelSelector: "view=visible"}
	fs, err := New(r, opts).analyzePDBs(NewScanContext(context.Background(), r, opts))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, f := range fs {
		if f.Status == "PDBOverlap" {
			found = true
		}
	}
	if !found {
		t.Fatalf("filter hid overlapping PDB: %+v", fs)
	}
}
