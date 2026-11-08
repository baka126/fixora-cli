package repo

import (
	"bytes"
	"encoding/json"
	"errors"
	"github.com/fixora/kubectl-fixora/internal/image"
	"github.com/fixora/kubectl-fixora/internal/kube"
	"gopkg.in/yaml.v3"
	"io"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/strategicpatch"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
)

type HelmValuesEdit struct {
	Pointer string
	Value   any `json:"-" yaml:"-"`
}
type HelmValuesCandidate struct {
	edits  []HelmValuesEdit
	edited []byte
}
type HelmMapping struct{ FieldPointer, ValuesPointer string }
type helmDelta struct {
	path          string
	before, after any
}
type helmEditOption struct {
	edit   HelmValuesEdit
	covers []int
}

func ProposeHelmValues(in *HelmInputs, inv HelmInventory, key ResourceKey, patch string, mappings []HelmMapping) ([]HelmValuesCandidate, error) {
	if in == nil {
		return nil, errors.New("captured Helm inputs required")
	}
	base, err := inv.Target(key)
	if err != nil {
		return nil, err
	}
	expected, err := expectedHelmTarget(base, patch)
	if err != nil {
		return nil, err
	}
	var node yaml.Node
	if err = decodeHelmValues(in.files[in.context.EditValuesPath], &node); err != nil {
		return nil, err
	}
	if len(mappings) > 0 {
		edits := []HelmValuesEdit{}
		for _, m := range mappings {
			old, ok := helmPointer(base, m.FieldPointer)
			next, exists := helmPointer(expected, m.FieldPointer)
			if (!ok && !exists) || (ok == exists && reflect.DeepEqual(old, next)) {
				return nil, errors.New("explicit mapping does not name a changed target field")
			}
			if _, err = helmPointerParts(m.ValuesPointer); err != nil || sensitiveHelmPointer(m.ValuesPointer) || sensitiveHelmValue(next) {
				return nil, errors.New("values mapping requires source review")
			}
			edits = append(edits, HelmValuesEdit{m.ValuesPointer, next})
		}
		v, err := makeHelmCandidate(in, edits)
		if err != nil {
			return nil, err
		}
		return []HelmValuesCandidate{v}, nil
	}
	effective, err := effectiveHelmValues(in)
	if err != nil {
		return nil, err
	}
	values := []helmDelta{}
	walkHelmValues(effective, "", &values)
	diffs, nodes := helmTargetChanges(base, expected, "")
	if len(diffs) == 0 {
		return nil, errors.New("source already renders the intended change")
	}
	options := []helmEditOption{}
	for _, n := range nodes {
		cover := []int{}
		for j, d := range diffs {
			if d.path == n.path || strings.HasPrefix(d.path, n.path+"/") {
				cover = append(cover, j)
			}
		}
		if len(cover) == 0 {
			continue
		}
		for _, v := range values {
			if v.path == "" || sensitiveHelmPointer(v.path) || sensitiveHelmValue(n.after) {
				continue
			}
			if helmHash(v.before) == helmHash(n.before) {
				options = append(options, helmEditOption{HelmValuesEdit{v.path, n.after}, cover})
			}
		}
		if strings.HasSuffix(n.path, "/image") {
			old, ok := n.before.(string)
			next, okN := n.after.(string)
			if ok && okN {
				for _, pair := range helmImageChanges(old, next) {
					for _, v := range values {
						if !sensitiveHelmPointer(v.path) && v.before == pair.before {
							options = append(options, helmEditOption{HelmValuesEdit{v.path, pair.after}, cover})
						}
					}
				}
			}
		}
	}
	results := []HelmValuesCandidate{}
	seen := map[string]bool{}
	add := func(edits []HelmValuesEdit) error {
		c, err := makeHelmCandidate(in, edits)
		if err != nil {
			return nil
		}
		hash := helmHash(c.edited)
		if !seen[hash] {
			seen[hash] = true
			results = append(results, c)
		}
		if len(results) > 32 {
			return errors.New("values mapping has too many combinations; select explicit mappings")
		}
		return nil
	}
	var search func([]bool, []HelmValuesEdit) error
	search = func(done []bool, edits []HelmValuesEdit) error {
		first := -1
		for j, v := range done {
			if !v {
				first = j
				break
			}
		}
		if first < 0 {
			return add(edits)
		}
		for _, o := range options {
			matches := false
			for _, j := range o.covers {
				if j == first {
					matches = true
				}
			}
			if !matches {
				continue
			}
			d := append([]bool(nil), done...)
			for _, j := range o.covers {
				d[j] = true
			}
			if err := search(d, append(append([]HelmValuesEdit(nil), edits...), o.edit)); err != nil {
				return err
			}
		}
		return nil
	}
	if err = search(make([]bool, len(diffs)), nil); err != nil {
		return nil, err
	}
	// For a single changed image, include pairs of its observed component mappings.
	if len(diffs) == 1 && strings.HasSuffix(diffs[0].path, "/image") {
		for i, a := range options {
			for _, b := range options[i+1:] {
				if a.edit.Pointer != b.edit.Pointer {
					if err = add([]HelmValuesEdit{a.edit, b.edit}); err != nil {
						return nil, err
					}
				}
			}
		}
	}
	if len(results) == 0 {
		return nil, errors.New("no values mapping could be proposed; review the owning template")
	}
	return results, nil
}
func expectedHelmTarget(base map[string]any, patch string) (map[string]any, error) {
	var n yaml.Node
	if err := decodeHelmValues([]byte(patch), &n); err != nil {
		return nil, err
	}
	var obj map[string]any
	_ = n.Decode(&obj)
	kind := stringField(base, "kind")
	if err := kube.ValidateWorkloadPatchEnvelope(obj, kind); err != nil {
		return nil, errors.New("candidate is not a supported workload patch")
	}
	for _, key := range []string{"apiVersion", "kind"} {
		if obj[key] != base[key] {
			return nil, errors.New("candidate resource identity differs")
		}
	}
	bm, _ := base["metadata"].(map[string]any)
	pm, _ := obj["metadata"].(map[string]any)
	if pm["name"] != bm["name"] || pm["namespace"] != bm["namespace"] || len(pm) != 2 {
		return nil, errors.New("candidate metadata differs from exact target")
	}
	var schema any
	switch kind {
	case "Deployment":
		schema = appsv1.Deployment{}
	case "StatefulSet":
		schema = appsv1.StatefulSet{}
	case "DaemonSet":
		schema = appsv1.DaemonSet{}
	case "ReplicaSet":
		schema = appsv1.ReplicaSet{}
	case "Pod":
		schema = corev1.Pod{}
	case "Job":
		schema = batchv1.Job{}
	case "CronJob":
		schema = batchv1.CronJob{}
	default:
		return nil, errors.New("unsupported Helm target")
	}
	br, _ := json.Marshal(base)
	pr, _ := json.Marshal(obj)
	data, err := strategicpatch.StrategicMergePatch(br, pr, schema)
	if err != nil {
		return nil, errors.New("workload patch cannot be merged")
	}
	var out map[string]any
	_ = json.Unmarshal(data, &out)
	return out, nil
}
func decodeHelmValues(data []byte, n *yaml.Node) error {
	d := yaml.NewDecoder(bytes.NewReader(data))
	if d.Decode(n) != nil || len(n.Content) != 1 || n.Content[0].Kind != yaml.MappingNode {
		return errors.New("values/patch must be a YAML mapping")
	}
	if err := checkHelmNode(n); err != nil {
		return err
	}
	var more yaml.Node
	if err := d.Decode(&more); err != io.EOF {
		return errors.New("values/patch must contain one document")
	}
	return nil
}
func effectiveHelmValues(in *HelmInputs) (map[string]any, error) {
	out := map[string]any{}
	for _, p := range append([]string{filepath.Join(in.context.ChartPath, "values.yaml")}, in.context.ValuesFiles...) {
		data, ok := in.files[p]
		if !ok {
			continue
		}
		var n yaml.Node
		if err := decodeHelmValues(data, &n); err != nil {
			return nil, err
		}
		var m map[string]any
		_ = n.Decode(&m)
		out = mergeHelmValues(out, m)
	}
	return out, nil
}
func mergeHelmValues(old, next map[string]any) map[string]any {
	out := copyHelmObject(old)
	if out == nil {
		out = map[string]any{}
	}
	for k, v := range next {
		if v == nil {
			delete(out, k)
			continue
		}
		if m, ok := v.(map[string]any); ok {
			prior, _ := out[k].(map[string]any)
			out[k] = mergeHelmValues(prior, m)
		} else {
			out[k] = v
		}
	}
	return out
}
func helmTargetChanges(a, b any, path string) ([]helmDelta, []helmDelta) {
	if helmHash(a) == helmHash(b) {
		return nil, nil
	}
	node := helmDelta{path, a, b}
	leaves := []helmDelta{}
	nodes := []helmDelta{node}
	am, aMap := a.(map[string]any)
	bm, bMap := b.(map[string]any)
	if aMap && bMap {
		keys := map[string]bool{}
		for k := range am {
			keys[k] = true
		}
		for k := range bm {
			keys[k] = true
		}
		for _, k := range sortedHelmKeys(keys) {
			l, n := helmTargetChanges(am[k], bm[k], path+"/"+escapeHelmPointer(k))
			leaves = append(leaves, l...)
			nodes = append(nodes, n...)
		}
	} else if aa, ok := a.([]any); ok {
		bb, ok := b.([]any)
		if !ok || len(aa) != len(bb) {
			return []helmDelta{node}, nodes
		}
		for j := range aa {
			l, n := helmTargetChanges(aa[j], bb[j], path+"/"+strconv.Itoa(j))
			leaves = append(leaves, l...)
			nodes = append(nodes, n...)
		}
	} else {
		leaves = append(leaves, node)
	}
	return leaves, nodes
}
func walkHelmValues(v any, path string, out *[]helmDelta) {
	*out = append(*out, helmDelta{path, v, nil})
	switch x := v.(type) {
	case map[string]any:
		keys := map[string]bool{}
		for k := range x {
			keys[k] = true
		}
		for _, k := range sortedHelmKeys(keys) {
			walkHelmValues(x[k], path+"/"+escapeHelmPointer(k), out)
		}
	case []any:
		for j, v := range x {
			walkHelmValues(v, path+"/"+strconv.Itoa(j), out)
		}
	}
}
func sortedHelmKeys(m map[string]bool) []string {
	keys := []string{}
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
func escapeHelmPointer(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "~", "~0"), "/", "~1")
}
func helmPointerParts(p string) ([]string, error) {
	if p == "" || !strings.HasPrefix(p, "/") {
		return nil, errors.New("mapping must use a non-root JSON pointer")
	}
	parts := strings.Split(p[1:], "/")
	for j, s := range parts {
		for k := 0; k < len(s); k++ {
			if s[k] == '~' && (k+1 == len(s) || (s[k+1] != '0' && s[k+1] != '1')) {
				return nil, errors.New("invalid JSON pointer")
			}
		}
		parts[j] = strings.ReplaceAll(strings.ReplaceAll(s, "~1", "/"), "~0", "~")
	}
	return parts, nil
}
func helmPointer(obj any, p string) (any, bool) {
	parts, err := helmPointerParts(p)
	if err != nil {
		return nil, false
	}
	for _, part := range parts {
		switch v := obj.(type) {
		case map[string]any:
			var ok bool
			obj, ok = v[part]
			if !ok {
				return nil, false
			}
		case []any:
			j, err := strconv.Atoi(part)
			if err != nil || j < 0 || j >= len(v) {
				return nil, false
			}
			obj = v[j]
		default:
			return nil, false
		}
	}
	return obj, true
}
func makeHelmCandidate(in *HelmInputs, edits []HelmValuesEdit) (HelmValuesCandidate, error) {
	var n yaml.Node
	if err := decodeHelmValues(in.files[in.context.EditValuesPath], &n); err != nil {
		return HelmValuesCandidate{}, err
	}
	seen := []string{}
	for _, edit := range edits {
		for _, p := range seen {
			if p == edit.Pointer || strings.HasPrefix(p, edit.Pointer+"/") || strings.HasPrefix(edit.Pointer, p+"/") {
				return HelmValuesCandidate{}, errors.New("overlapping values edits")
			}
		}
		seen = append(seen, edit.Pointer)
		parts, err := helmPointerParts(edit.Pointer)
		if err != nil {
			return HelmValuesCandidate{}, err
		}
		if err = setHelmNode(n.Content[0], parts, edit.Value); err != nil {
			return HelmValuesCandidate{}, err
		}
	}
	var data bytes.Buffer
	e := yaml.NewEncoder(&data)
	e.SetIndent(2)
	if err := e.Encode(&n); err != nil {
		return HelmValuesCandidate{}, err
	}
	_ = e.Close()
	return HelmValuesCandidate{append([]HelmValuesEdit(nil), edits...), data.Bytes()}, nil
}
func setHelmNode(n *yaml.Node, path []string, value any) error {
	if len(path) == 0 {
		comment := n.HeadComment
		line := n.LineComment
		foot := n.FootComment
		if err := n.Encode(value); err != nil {
			return err
		}
		n.HeadComment, n.LineComment, n.FootComment = comment, line, foot
		return nil
	}
	switch n.Kind {
	case yaml.MappingNode:
		for j := 0; j < len(n.Content); j += 2 {
			if n.Content[j].Value == path[0] {
				return setHelmNode(n.Content[j+1], path[1:], value)
			}
		}
		key := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: path[0]}
		next := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		n.Content = append(n.Content, key, next)
		return setHelmNode(next, path[1:], value)
	case yaml.SequenceNode:
		j, err := strconv.Atoi(path[0])
		if err != nil || j < 0 || j >= len(n.Content) {
			return errors.New("values list mapping is ambiguous")
		}
		return setHelmNode(n.Content[j], path[1:], value)
	default:
		return errors.New("values mapping crosses a scalar")
	}
}
func sensitiveHelmPointer(p string) bool {
	p = strings.ToLower(p)
	for _, part := range []string{"password", "secret", "token", "credential", "privatekey", "private_key", "api_key"} {
		if strings.Contains(p, part) {
			return true
		}
	}
	return false
}
func sensitiveHelmValue(v any) bool {
	switch x := v.(type) {
	case map[string]any:
		if name, ok := x["name"].(string); ok && sensitiveHelmPointer(name) && x["value"] != nil {
			return true
		}
		for k, v := range x {
			if sensitiveHelmPointer(k) || sensitiveHelmValue(v) {
				return true
			}
		}
	case []any:
		for _, v := range x {
			if sensitiveHelmValue(v) {
				return true
			}
		}
	}
	return false
}
func helmImageChanges(old, next string) []helmDelta {
	if _, err := image.Repository(old); err != nil {
		return nil
	}
	if _, err := image.Repository(next); err != nil {
		return nil
	}
	split := func(s string) (string, string) {
		if i := strings.Index(s, "@"); i >= 0 {
			return s[:i], s[i+1:]
		}
		if i := strings.LastIndex(s, ":"); i > strings.LastIndex(s, "/") {
			return s[:i], s[i+1:]
		}
		return s, ""
	}
	ob, ot := split(old)
	nb, nt := split(next)
	out := []helmDelta{}
	if ob != nb {
		out = append(out, helmDelta{before: ob, after: nb})
	}
	if ot != "" && nt != "" && ot != nt {
		out = append(out, helmDelta{before: ot, after: nt})
	}
	return out
}
