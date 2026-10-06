package repairpolicy

import (
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"strings"

	"github.com/fixora/kubectl-fixora/internal/kube"
	"gopkg.in/yaml.v3"
	"k8s.io/apimachinery/pkg/util/validation"
)

func ReadBaseline(path string) (*Authority, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("read repair baseline: %w", err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 1024*1024+1))
	if err != nil {
		return nil, err
	}
	if len(data) > 1024*1024 {
		return nil, errors.New("repair baseline exceeds 1MiB")
	}
	decoder := yaml.NewDecoder(strings.NewReader(string(data)))
	var obj map[string]any
	if err = decoder.Decode(&obj); err != nil {
		return nil, fmt.Errorf("parse repair baseline: %w", err)
	}
	var extra any
	if err = decoder.Decode(&extra); err != io.EOF {
		return nil, errors.New("repair baseline must contain exactly one YAML document")
	}
	if secretReference(obj) {
		return nil, errors.New("repair baseline must not reference Secrets")
	}
	kind, _ := obj["kind"].(string)
	meta, _ := obj["metadata"].(map[string]any)
	name, _ := meta["name"].(string)
	ns, _ := meta["namespace"].(string)
	if kube.WorkloadVersion(kind) == "" || obj["apiVersion"] != kube.WorkloadVersion(kind) || name == "" || ns == "" || len(validation.IsDNS1123Subdomain(name)) > 0 || len(validation.IsDNS1123Label(ns)) > 0 {
		return nil, errors.New("repair baseline requires a valid workload identity")
	}
	if _, ok := kube.WorkloadPodSpec(obj); !ok {
		return nil, errors.New("repair baseline requires an unambiguous Pod template")
	}
	obj = copyObject(obj)
	return &Authority{object: obj, digest: digest(obj)}, nil
}
func secretReference(v any) bool {
	switch x := v.(type) {
	case map[string]any:
		for k, v := range x {
			switch k {
			case "secretKeyRef", "secretRef", "secret", "imagePullSecrets":
				return true
			}
			if secretReference(v) {
				return true
			}
		}
	case []any:
		for _, v := range x {
			if secretReference(v) {
				return true
			}
		}
	}
	return false
}

// A container name in the path selects by identity, not list position.
func (a *Authority) authorizes(path []string, value any) bool {
	if a == nil || a.digest == "" || a.digest != digest(a.object) {
		return false
	}
	spec, ok := kube.WorkloadPodSpec(a.object)
	if !ok {
		return false
	}
	var cursor any = spec
	for _, key := range path {
		switch v := cursor.(type) {
		case map[string]any:
			var exists bool
			cursor, exists = v[key]
			if !exists {
				return false
			}
		case []any:
			found := false
			for _, raw := range v {
				item, ok := raw.(map[string]any)
				if ok && item["name"] == key {
					if found {
						return false
					}
					cursor = item
					found = true
				}
			}
			if !found {
				return false
			}
		default:
			return false
		}
	}
	// Normalize []string vs decoded []any and numeric YAML/JSON values.
	return reflect.DeepEqual(copyValue(cursor), copyValue(value))
}
func copyValue(v any) any { // JSON normalization also prevents retaining mutable input.
	raw := map[string]any{"value": v}
	return copyObject(raw)["value"]
}
