package repo

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/fixora/kubectl-fixora/internal/kube"
	"gopkg.in/yaml.v3"
	"io"
	"k8s.io/apimachinery/pkg/api/resource"
	"strings"
)

type ResourceKey struct{ APIVersion, Kind, Namespace, Name string }
type HelmScopeFunc func(ResourceKey) (bool, error)
type HelmInventory struct {
	objects map[ResourceKey]map[string]any
}

func ParseHelmInventory(data []byte, namespace string, scope HelmScopeFunc) (HelmInventory, error) {
	inv := HelmInventory{objects: map[ResourceKey]map[string]any{}}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	for {
		var node yaml.Node
		err := dec.Decode(&node)
		if err == io.EOF {
			break
		}
		if err != nil {
			return inv, errors.New("invalid rendered YAML")
		}
		if len(node.Content) == 0 || node.Content[0].Tag == "!!null" {
			continue
		}
		if err = checkHelmNode(&node); err != nil {
			return inv, err
		}
		var obj map[string]any
		if node.Decode(&obj) != nil {
			return inv, errors.New("render document is not an object")
		}
		meta, _ := obj["metadata"].(map[string]any)
		key := ResourceKey{stringField(obj, "apiVersion"), stringField(obj, "kind"), stringField(meta, "namespace"), stringField(meta, "name")}
		if key.APIVersion == "" || key.Kind == "" || key.Name == "" || key.Kind == "List" {
			return inv, errors.New("render requires named individual resource documents")
		}
		if scope == nil {
			return inv, errors.New("resource scope is unavailable")
		}
		namespaced, err := scope(key)
		if err != nil {
			return inv, errors.New("resource scope is unavailable")
		}
		if namespaced && key.Namespace == "" {
			key.Namespace = namespace
			meta["namespace"] = namespace
		}
		if !namespaced && key.Namespace != "" {
			return inv, errors.New("cluster resource has a namespace")
		}
		if namespaced && key.Namespace == "" {
			return inv, errors.New("render namespace is missing")
		}
		if _, exists := inv.objects[key]; exists {
			return inv, errors.New("duplicate rendered resource identity")
		}
		inv.objects[key] = obj
	}
	if len(inv.objects) == 0 {
		return inv, errors.New("empty rendered inventory")
	}
	return inv, nil
}
func (i HelmInventory) Digest() string {
	m := map[string]any{}
	for k, v := range i.objects {
		key, _ := json.Marshal(k)
		m[string(key)] = semanticHelm(v, "")
	}
	return helmHash(m)
}
func (i HelmInventory) Target(key ResourceKey) (map[string]any, error) {
	obj := i.objects[key]
	if obj == nil {
		return nil, errors.New("exact target not found in rendered chart")
	}
	return copyHelmObject(obj), nil
}
func checkHelmNode(n *yaml.Node) error {
	if n.Kind == yaml.AliasNode || n.Anchor != "" {
		return errors.New("YAML aliases require source review")
	}
	if n.Kind == yaml.MappingNode {
		seen := map[string]bool{}
		for j := 0; j < len(n.Content); j += 2 {
			k := n.Content[j]
			if k.Tag != "!!str" || k.Value == "<<" || seen[k.Value] {
				return errors.New("YAML has unsupported or duplicate mapping keys")
			}
			seen[k.Value] = true
		}
	}
	for _, child := range n.Content {
		if err := checkHelmNode(child); err != nil {
			return err
		}
	}
	return nil
}
func stringField(m map[string]any, k string) string { s, _ := m[k].(string); return s }
func helmHash(v any) string {
	data, _ := json.Marshal(v)
	h := sha256.Sum256(data)
	return fmt.Sprintf("%x", h)
}
func copyHelmObject(v map[string]any) map[string]any {
	data, _ := json.Marshal(v)
	var out map[string]any
	_ = json.Unmarshal(data, &out)
	return out
}
func semanticHelm(v any, path string) any {
	switch x := v.(type) {
	case map[string]any:
		out := map[string]any{}
		for k, v := range x {
			out[k] = semanticHelm(v, path+"/"+k)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for j, v := range x {
			out[j] = semanticHelm(v, path)
		}
		return out
	default:
		if strings.Contains(path, "/resources/requests/") || strings.Contains(path, "/resources/limits/") {
			if q, err := resource.ParseQuantity(fmt.Sprint(v)); err == nil {
				return q.String()
			}
		}
		return v
	}
}
func helmPodSpec(obj map[string]any) map[string]any { s, _ := kube.WorkloadPodSpec(obj); return s }
