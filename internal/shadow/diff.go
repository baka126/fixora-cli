package shadow

import (
	"fmt"

	"github.com/hexops/gotextdiff"
	"github.com/hexops/gotextdiff/myers"
	"github.com/hexops/gotextdiff/span"
	"sigs.k8s.io/yaml"
)

func CloneDiff(original, clone any) string {
	left, _ := yaml.Marshal(original)
	right, _ := yaml.Marshal(clone)
	return unifiedDiff("original-pod", "shadow-pod", string(left), string(right))
}

func PatchDiff(resource, patch string) string {
	return unifiedDiff("current/"+resource, "proposed/"+resource, "", patch)
}

func unifiedDiff(from, to, left, right string) string {
	edits := myers.ComputeEdits(span.URIFromPath(from), left, right)
	diff := fmt.Sprint(gotextdiff.ToUnified(from, to, left, edits))
	return diff
}
