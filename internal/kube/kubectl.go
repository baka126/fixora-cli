package kube

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"
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
	args := []string{"get", "pods", "--chunk-size=500"}
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
	args := []string{"get", resource, "--chunk-size=500"}
	args = append(args, scopeArgs(namespace, allNS)...)
	args = append(args, "-o", "json")
	if err := k.GetJSON(ctx, &list, args...); err != nil {
		return nil, err
	}
	return list.Items, nil
}

func (k Kubectl) GetEvents(ctx context.Context, namespace string, fieldSelector string) ([]Event, error) {
	var events EventList
	args := []string{"get", "events", "--chunk-size=500"}
	if namespace != "" {
		args = append(args, "-n", namespace)
	} else {
		args = append(args, "-A")
	}
	if fieldSelector != "" {
		args = append(args, "--field-selector", fieldSelector)
	}
	args = append(args, "-o", "json")
	if err := k.GetJSON(ctx, &events, args...); err != nil {
		return nil, err
	}
	return events.Items, nil
}

func (k Kubectl) GetNodes(ctx context.Context) ([]Node, error) {
	var nodes NodeList
	err := k.GetJSON(ctx, &nodes, "get", "nodes", "--chunk-size=500", "-o", "json")
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

func (k Kubectl) RolloutStatus(ctx context.Context, kind, name, namespace string, timeout time.Duration) (bool, string, error) {
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	args := []string{"rollout", "status", kind + "/" + name, "--timeout=" + timeout.String()}
	if namespace != "" {
		args = append(args, "-n", namespace)
	}
	out, err := k.Run(ctx, args...)
	return classifyRolloutResult(string(out), err)
}

// classifyRolloutResult maps `kubectl rollout status` output/exit into the
// RolloutChecker contract: (true,_,nil) rolled out; (false,_,nil) did not
// finish (timeout or progress deadline); (false,_,err) a real execution error.
func classifyRolloutResult(out string, runErr error) (bool, string, error) {
	text := strings.TrimSpace(out)
	if runErr == nil {
		return true, text, nil
	}
	combined := strings.ToLower(text + " " + runErr.Error())
	if strings.Contains(combined, "timed out") || strings.Contains(combined, "progress deadline") {
		if text == "" {
			text = strings.TrimSpace(runErr.Error())
		}
		return false, text, nil
	}
	return false, text, runErr
}

type jobConditionJSON struct {
	Type   string `json:"type"`
	Status string `json:"status"`
}

type jobStatusJSON struct {
	Succeeded  int                `json:"succeeded"`
	Failed     int                `json:"failed"`
	Active     int                `json:"active"`
	Conditions []jobConditionJSON `json:"conditions"`
}

type jobJSON struct {
	Metadata ObjectMeta    `json:"metadata"`
	Status   jobStatusJSON `json:"status"`
}

type ownerRefJSON struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
}

type jobListItemJSON struct {
	Metadata struct {
		Name              string         `json:"name"`
		CreationTimestamp string         `json:"creationTimestamp"`
		OwnerReferences   []ownerRefJSON `json:"ownerReferences"`
	} `json:"metadata"`
	Status jobStatusJSON `json:"status"`
}

type jobListJSON struct {
	Items []jobListItemJSON `json:"items"`
}

type cronJobJSON struct {
	Spec struct {
		Schedule string `json:"schedule"`
		Suspend  *bool  `json:"suspend"`
	} `json:"spec"`
	Status struct {
		LastSuccessfulTime string `json:"lastSuccessfulTime"`
	} `json:"status"`
}

// classifyJobStatus maps a Job's status into the completion contract: a
// Complete=True condition is success, a Failed=True condition is terminal
// failure, anything else is still pending.
func classifyJobStatus(s jobStatusJSON) JobState {
	state := JobState{Detail: fmt.Sprintf("succeeded %d, failed %d, active %d", s.Succeeded, s.Failed, s.Active)}
	for _, c := range s.Conditions {
		if c.Status != "True" {
			continue
		}
		switch c.Type {
		case "Complete":
			state.Complete = true
		case "Failed":
			state.Failed = true
		}
	}
	return state
}

func getArgs(verb, resource, name, namespace string) []string {
	args := []string{verb, resource, name}
	if namespace != "" {
		args = append(args, "-n", namespace)
	}
	return append(args, "-o", "json")
}

// JobStatus polls the live Job until it completes, fails terminally, or the
// timeout elapses (a still-running Job at timeout returns Complete=false,
// Failed=false — the caller treats that as non-blocking).
func (k Kubectl) JobStatus(ctx context.Context, name, namespace string, timeout time.Duration) (JobState, error) {
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	pollCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var last JobState
	for {
		var j jobJSON
		if err := k.GetJSON(pollCtx, &j, getArgs("get", "job", name, namespace)...); err != nil {
			if pollCtx.Err() != nil {
				return last, nil
			}
			return JobState{}, err
		}
		last = classifyJobStatus(j.Status)
		if last.Complete || last.Failed {
			return last, nil
		}
		if sleepErr := sleepContext(pollCtx, 2*time.Second); sleepErr != nil {
			return last, nil // timeout/cancel: report the last pending state
		}
	}
}

// CronJobStatus reads the applied CronJob plus the most-recent Job it owns.
// Validate-only: it triggers nothing.
func (k Kubectl) CronJobStatus(ctx context.Context, name, namespace string) (CronJobState, error) {
	var cj cronJobJSON
	if err := k.GetJSON(ctx, &cj, getArgs("get", "cronjob", name, namespace)...); err != nil {
		return CronJobState{}, err
	}
	state := CronJobState{
		Suspended:      cj.Spec.Suspend != nil && *cj.Spec.Suspend,
		Schedule:       cj.Spec.Schedule,
		LastSuccessful: cj.Status.LastSuccessfulTime,
	}
	failed, detail, err := k.mostRecentOwnedJobFailed(ctx, name, namespace)
	if err != nil {
		// A failed read must propagate, not be demoted to "no recent failure" —
		// the caller maps the error to CronJobUnknown rather than CronJobHealthy.
		return CronJobState{}, err
	}
	state.RecentJobFailed = failed
	state.Detail = detail
	return state, nil
}

func (k Kubectl) mostRecentOwnedJobFailed(ctx context.Context, cronName, namespace string) (bool, string, error) {
	args := []string{"get", "jobs"}
	if namespace != "" {
		args = append(args, "-n", namespace)
	}
	args = append(args, "-o", "json")
	var list jobListJSON
	if err := k.GetJSON(ctx, &list, args...); err != nil {
		return false, "", err
	}
	newestTime := ""
	newest := jobListItemJSON{}
	found := false
	for _, item := range list.Items {
		owned := false
		for _, o := range item.Metadata.OwnerReferences {
			if o.Kind == "CronJob" && o.Name == cronName {
				owned = true
				break
			}
		}
		if !owned {
			continue
		}
		if item.Metadata.CreationTimestamp > newestTime {
			newestTime = item.Metadata.CreationTimestamp
			newest = item
			found = true
		}
	}
	if !found {
		return false, "no jobs created by this CronJob yet", nil
	}
	st := classifyJobStatus(newest.Status)
	return st.Failed, "most recent job " + newest.Metadata.Name + ": " + st.Detail, nil
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
	if err != nil && kubectlErrorOutput(text) {
		return "", fmt.Errorf("kubectl diff: %w: %s", err, text)
	}
	if err != nil && text == "" {
		return "", err
	}
	return text, nil
}

func kubectlErrorOutput(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	return strings.HasPrefix(value, "error:") || strings.Contains(value, "\nerror:")
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
	var err error
	for i := 0; i < 3; i++ {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		out, runErr := k.Run(ctx, args...)
		if runErr == nil {
			return json.Unmarshal(out, target)
		}
		err = runErr
		if i < 2 {
			if sleepErr := sleepContext(ctx, time.Duration(1<<i)*100*time.Millisecond); sleepErr != nil {
				return sleepErr
			}
		}
	}
	return err
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
