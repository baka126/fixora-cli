package kube

import (
	"encoding/json"
	"fmt"
	"k8s.io/apimachinery/pkg/util/yaml"
	"os"
)

// GuardedApplyFile binds an update to the object observed immediately before
// confirmation. resourceVersion protects against concurrent edits/replacement;
// uid pins identity. Dry-run and apply must use this same private file.
// The portable source proposal is left unchanged.
func GuardedApplyFile(source, uid, version string) (string, func(), error) {
	noop := func() {}
	if uid == "" || version == "" {
		return "", noop, fmt.Errorf("target UID/resourceVersion preconditions are required")
	}
	data, err := os.ReadFile(source)
	if err != nil {
		return "", noop, err
	}
	data, err = yaml.ToJSON(data)
	if err != nil {
		return "", noop, err
	}
	var obj map[string]any
	if err = json.Unmarshal(data, &obj); err != nil {
		return "", noop, err
	}
	meta, ok := obj["metadata"].(map[string]any)
	if !ok {
		return "", noop, fmt.Errorf("patch metadata is missing")
	}
	meta["uid"], meta["resourceVersion"] = uid, version
	data, err = json.Marshal(obj)
	if err != nil {
		return "", noop, err
	}
	file, err := os.CreateTemp("", "fixora-guarded-apply-*.json")
	if err != nil {
		return "", noop, err
	}
	cleanup := func() { _ = os.Remove(file.Name()) }
	if _, err = file.Write(data); err != nil {
		_ = file.Close()
		cleanup()
		return "", noop, err
	}
	if err = file.Close(); err != nil {
		cleanup()
		return "", noop, err
	}
	return file.Name(), cleanup, nil
}
