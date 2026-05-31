package kube

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

type Kubectl struct {
	Context       string
	LogTail       int
	LogLimitBytes int
}

type Status struct {
	KubectlAvailable bool   `json:"kubectlAvailable"`
	Context          string `json:"context"`
	ServerVersion    string `json:"serverVersion,omitempty"`
	Namespace        string `json:"namespace,omitempty"`
}

type DoctorCheck struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
}

type DoctorReport struct {
	Score  int           `json:"score"`
	Checks []DoctorCheck `json:"checks"`
}

func NewKubectl(context string) Kubectl {
	return Kubectl{Context: context, LogTail: 120, LogLimitBytes: 24000}
}

func (k Kubectl) Status(ctx context.Context) (Status, error) {
	status := Status{}
	if _, err := exec.LookPath("kubectl"); err != nil {
		return status, fmt.Errorf("kubectl not found in PATH")
	}
	status.KubectlAvailable = true
	if out, err := k.Run(ctx, "config", "current-context"); err == nil {
		status.Context = strings.TrimSpace(string(out))
	}
	if out, err := k.Run(ctx, "version", "--short"); err == nil {
		status.ServerVersion = strings.TrimSpace(string(out))
	}
	if out, err := k.Run(ctx, "config", "view", "--minify", "-o", "jsonpath={..namespace}"); err == nil {
		status.Namespace = strings.TrimSpace(string(out))
	}
	return status, nil
}

func (k Kubectl) Doctor(ctx context.Context, namespace string, allNS bool) ([]DoctorCheck, error) {
	checks := []DoctorCheck{}
	add := func(name string, err error, detail string) {
		status := "ok"
		if err != nil {
			status = "error"
			detail = err.Error()
		}
		checks = append(checks, DoctorCheck{Name: name, Status: status, Detail: detail})
	}
	_, err := exec.LookPath("kubectl")
	add("kubectl binary", err, "kubectl is available")
	_, err = k.Run(ctx, authCanIArgs("pods", namespace, allNS)...)
	add("pods read", err, "can read pods")
	_, err = k.Run(ctx, authCanIArgs("events", namespace, allNS)...)
	add("events read", err, "can read events")
	_, err = k.Run(ctx, authCanIArgs("pods/log", namespace, allNS)...)
	add("logs read", err, "can read pod logs")
	_, err = k.Run(ctx, "get", "nodes", "-o", "name")
	add("nodes read", err, "can read node metadata")
	_, err = k.Run(ctx, "get", "helmreleases.helm.toolkit.fluxcd.io", "-A", "-o", "name")
	add("Flux HelmRelease CRD", err, "Flux HelmRelease objects are readable")
	_, err = k.Run(ctx, "get", "applications.argoproj.io", "-A", "-o", "name")
	add("ArgoCD Application CRD", err, "ArgoCD Application objects are readable")
	return checks, nil
}

func (k Kubectl) DoctorReport(ctx context.Context, namespace string, allNS bool) (DoctorReport, error) {
	checks, err := k.Doctor(ctx, namespace, allNS)
	if err != nil {
		return DoctorReport{}, err
	}
	total := len(checks)
	ok := 0
	for _, check := range checks {
		if check.Status == "ok" {
			ok++
		}
	}
	score := 0
	if total > 0 {
		score = ok * 100 / total
	}
	return DoctorReport{Score: score, Checks: checks}, nil
}

func (k Kubectl) GetPods(ctx context.Context, namespace string, allNS bool) (PodList, error) {
	var pods PodList
	args := []string{"get", "pods"}
	args = append(args, scopeArgs(namespace, allNS)...)
	args = append(args, "-o", "json")
	err := k.GetJSON(ctx, &pods, args...)
	return pods, err
}

func (k Kubectl) GetPod(ctx context.Context, namespace, name string) (Pod, error) {
	var pod Pod
	err := k.GetJSON(ctx, &pod, "get", "pod", name, "-n", namespace, "-o", "json")
	return pod, err
}

func (k Kubectl) GetResource(ctx context.Context, namespace, resource string) (map[string]any, error) {
	var obj map[string]any
	args := []string{"get", resource}
	if namespace != "" {
		args = append(args, "-n", namespace)
	}
	args = append(args, "-o", "json")
	err := k.GetJSON(ctx, &obj, args...)
	return obj, err
}

func (k Kubectl) GetResourceItems(ctx context.Context, namespace string, allNS bool, resource string) ([]map[string]any, error) {
	var list struct {
		Items []map[string]any `json:"items"`
	}
	args := []string{"get", resource}
	args = append(args, scopeArgs(namespace, allNS)...)
	args = append(args, "-o", "json")
	if err := k.GetJSON(ctx, &list, args...); err != nil {
		return nil, err
	}
	return list.Items, nil
}

func (k Kubectl) GetEvents(ctx context.Context, namespace string) ([]Event, error) {
	var events EventList
	args := []string{"get", "events"}
	if namespace != "" {
		args = append(args, "-n", namespace)
	} else {
		args = append(args, "-A")
	}
	args = append(args, "-o", "json")
	if err := k.GetJSON(ctx, &events, args...); err != nil {
		return nil, err
	}
	return events.Items, nil
}

func (k Kubectl) GetNodes(ctx context.Context) ([]Node, error) {
	var nodes NodeList
	err := k.GetJSON(ctx, &nodes, "get", "nodes", "-o", "json")
	return nodes.Items, err
}

func (k Kubectl) Logs(ctx context.Context, namespace, pod string, previous bool) (string, error) {
	tail := k.LogTail
	if tail <= 0 {
		tail = 120
	}
	limitBytes := k.LogLimitBytes
	if limitBytes <= 0 {
		limitBytes = 24000
	}
	args := []string{"logs", pod, "-n", namespace, fmt.Sprintf("--tail=%d", tail), fmt.Sprintf("--limit-bytes=%d", limitBytes)}
	if previous {
		args = append(args, "--previous")
	}
	out, err := k.Run(ctx, args...)
	return string(out), err
}

func (k Kubectl) Apply(ctx context.Context, file string) error {
	_, err := k.Run(ctx, "apply", "-f", file)
	return err
}

func (k Kubectl) DryRunApply(ctx context.Context, file string) error {
	_, err := k.Run(ctx, "apply", "--dry-run=server", "-f", file)
	return err
}

func (k Kubectl) Diff(ctx context.Context, file string) (string, error) {
	full := []string{}
	if k.Context != "" {
		full = append(full, "--context", k.Context)
	}
	full = append(full, "diff", "-f", file)
	cmd := exec.CommandContext(ctx, "kubectl", full...)
	out, err := cmd.CombinedOutput()
	text := strings.TrimSpace(string(out))
	if err != nil && text == "" {
		return "", err
	}
	return text, nil
}

func (k Kubectl) AuthCanI(ctx context.Context, namespace, serviceAccount, verb, resource string) (string, error) {
	args := []string{"auth", "can-i", verb, resource}
	if serviceAccount != "" {
		args = append(args, "--as", "system:serviceaccount:"+namespace+":"+serviceAccount)
	}
	if namespace != "" {
		args = append(args, "-n", namespace)
	}
	out, err := k.Run(ctx, args...)
	return strings.TrimSpace(string(out)), err
}

func (k Kubectl) GetJSON(ctx context.Context, target any, args ...string) error {
	out, err := k.Run(ctx, args...)
	if err != nil {
		return err
	}
	return json.Unmarshal(out, target)
}

func (k Kubectl) Run(ctx context.Context, args ...string) ([]byte, error) {
	full := []string{}
	if k.Context != "" {
		full = append(full, "--context", k.Context)
	}
	full = append(full, args...)
	cmd := exec.CommandContext(ctx, "kubectl", full...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg != "" {
			return out, fmt.Errorf("%s", msg)
		}
		return out, err
	}
	return out, nil
}

func scopeArgs(namespace string, allNS bool) []string {
	if allNS || namespace == "" {
		return []string{"-A"}
	}
	return []string{"-n", namespace}
}

func authCanIArgs(resource, namespace string, allNS bool) []string {
	args := []string{"auth", "can-i", "get", resource}
	return append(args, scopeArgs(namespace, allNS)...)
}
