package infer

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"strconv"
	"strings"

	"github.com/fixora/kubectl-fixora/internal/analyzer"
	"github.com/fixora/kubectl-fixora/internal/fix"
	"github.com/fixora/kubectl-fixora/internal/kube"
	"k8s.io/apimachinery/pkg/util/validation"
)

func init() { register(dependencyInferrer{}) }

type dependencyInferrer struct{}

func (dependencyInferrer) Name() string { return "dependency" }
func (dependencyInferrer) Handles(p fix.Plan) bool {
	switch p.Status {
	case "CrashLoopBackOff", "DatabaseUnreachable", "DependencyTimeout", "ConnectionRefused":
		return true
	}
	return p.Strategy == "runtime" || p.Strategy == "collect-evidence"
}

// Infer repairs only a connection whose intended source the operator named on
// the owning Deployment. A ready in-cluster Service alone never establishes
// intent, and this path neither reads secrets nor creates dependencies.
func (dependencyInferrer) Infer(ctx context.Context, r kubeReader, f analyzer.Finding, _ fix.Plan) (Result, bool, error) {
	if r == nil || f.Recovered || f.ResourceKind != "Deployment" || f.Namespace == "" || f.PodName == "" || f.ResourceName == "" || !dependencyFailureLog(f) {
		return Result{}, false, nil
	}
	var pod kube.Pod
	if !dependencyRead(ctx, r, f.Namespace, "pod/"+f.PodName, &pod) || pod.Metadata.Name != f.PodName || pod.Metadata.Namespace != f.Namespace {
		return Result{}, false, nil
	}
	if len(pod.Metadata.OwnerRefs) != 1 || pod.Metadata.OwnerRefs[0].Kind != "ReplicaSet" {
		return Result{}, false, nil
	}
	rsName := pod.Metadata.OwnerRefs[0].Name
	var rs struct {
		Metadata kube.ObjectMeta `json:"metadata"`
	}
	if !dependencyRead(ctx, r, f.Namespace, "replicaset/"+rsName, &rs) || rs.Metadata.Name != rsName || rs.Metadata.Namespace != f.Namespace || len(rs.Metadata.OwnerRefs) != 1 || rs.Metadata.OwnerRefs[0].Kind != "Deployment" || rs.Metadata.OwnerRefs[0].Name != f.ResourceName {
		return Result{}, false, nil
	}
	var deployment struct {
		Metadata kube.ObjectMeta `json:"metadata"`
		Spec     struct {
			Template struct {
				Spec kube.PodSpec `json:"spec"`
			} `json:"template"`
		} `json:"spec"`
	}
	if !dependencyRead(ctx, r, f.Namespace, "deployment/"+f.ResourceName, &deployment) || deployment.Metadata.Name != f.ResourceName || deployment.Metadata.Namespace != f.Namespace {
		return Result{}, false, nil
	}
	source := deployment.Metadata.Annotations["fixora.io/connection-configmap"]
	if source == "" || len(validation.IsDNS1123Subdomain(source)) > 0 {
		return Result{}, false, nil
	}
	name := ContainerName(f)
	if name == "" {
		return Result{}, false, nil
	}
	failing := false
	for _, cs := range pod.Status.ContainerStatuses {
		termination := cs.LastState["terminated"]
		if current, ok := cs.State["terminated"]; ok {
			termination = current
		}
		if cs.Name == name && !cs.Ready && termination.ExitCode != 0 && termination.Reason != "OOMKilled" && (cs.State["waiting"].Reason == "CrashLoopBackOff" || cs.State["terminated"].ExitCode != 0) {
			failing = true
		}
	}
	if !failing {
		return Result{}, false, nil
	}
	var selected *kube.Container
	for i := range pod.Spec.Containers {
		if pod.Spec.Containers[i].Name == name {
			if selected != nil {
				return Result{}, false, nil
			}
			selected = &pod.Spec.Containers[i]
		}
	}
	if selected == nil || !dependencyNCCommand(*selected) || len(selected.EnvFrom) > 0 {
		return Result{}, false, nil
	}
	matched := 0
	for _, c := range deployment.Spec.Template.Spec.Containers {
		if c.Name == name && c.Image == selected.Image && reflect.DeepEqual(c.Command, selected.Command) && reflect.DeepEqual(c.Args, selected.Args) && reflect.DeepEqual(c.Env, selected.Env) {
			matched++
		}
	}
	if matched != 1 {
		return Result{}, false, nil
	}
	current := map[string]string{}
	for _, env := range selected.Env {
		if _, secret := env.ValueFrom["secretKeyRef"]; secret {
			return Result{}, false, nil
		}
		if env.Name != "DB_HOST" && env.Name != "DB_PORT" {
			continue
		}
		if _, exists := current[env.Name]; exists || len(env.ValueFrom) > 0 {
			return Result{}, false, nil
		}
		current[env.Name] = env.Value
	}
	if len(current) != 2 {
		return Result{}, false, nil
	}
	var config struct {
		Metadata kube.ObjectMeta   `json:"metadata"`
		Data     map[string]string `json:"data"`
	}
	if !dependencyRead(ctx, r, f.Namespace, "configmap/"+source, &config) || config.Metadata.Name != source || config.Metadata.Namespace != f.Namespace {
		return Result{}, false, nil
	}
	host, portText := config.Data["DB_HOST"], config.Data["DB_PORT"]
	serviceName, ok := dependencyServiceHost(host, f.Namespace)
	if !ok {
		return Result{}, false, nil
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 || strconv.Itoa(port) != portText {
		return Result{}, false, nil
	}
	var service struct {
		Metadata kube.ObjectMeta `json:"metadata"`
		Spec     struct {
			Type  string           `json:"type"`
			Ports []dependencyPort `json:"ports"`
		} `json:"spec"`
	}
	if !dependencyRead(ctx, r, f.Namespace, "service/"+serviceName, &service) || service.Metadata.Name != serviceName || service.Metadata.Namespace != f.Namespace || service.Spec.Type == "ExternalName" {
		return Result{}, false, nil
	}
	var target dependencyPort
	portsMatched := 0
	for _, p := range service.Spec.Ports {
		if p.Port == port && (p.Protocol == "" || p.Protocol == "TCP") {
			target = p
			portsMatched++
		}
	}
	if portsMatched != 1 {
		return Result{}, false, nil
	}
	slices, err := r.GetResourceItems(ctx, f.Namespace, false, "endpointslices.discovery.k8s.io")
	if err != nil {
		return Result{}, false, err
	}
	if !dependencyReadyEndpoint(slices, f.Namespace, serviceName, target) {
		return Result{}, false, nil
	}
	keys := []string{}
	for _, key := range []string{"DB_HOST", "DB_PORT"} {
		if current[key] != config.Data[key] {
			keys = append(keys, key)
		}
	}
	if len(keys) == 0 {
		return Result{}, false, nil
	}
	return Result{ConnectionConfig: &fix.ConnectionConfig{Container: name, ConfigMap: source, Keys: keys}, Guardrail: "inferred-connection-from-approved-configmap", Warning: fmt.Sprintf("The owning Deployment names ConfigMap %s as its connection source. Its database endpoint resolves to ready TCP Service %s:%d in namespace %s; shadow verification must confirm the client connects.", source, serviceName, port, f.Namespace)}, true, nil
}

func dependencyRead(ctx context.Context, r kubeReader, namespace, resource string, target any) bool {
	raw, err := r.GetResource(ctx, namespace, resource)
	if err != nil {
		return false
	}
	data, err := json.Marshal(raw)
	return err == nil && json.Unmarshal(data, target) == nil
}
func dependencyFailureLog(f analyzer.Finding) bool {
	for _, log := range f.Logs {
		text := strings.ToLower(log.Text)
		for _, signal := range []string{"connection refused", "bad address", "could not connect", "econnrefused", "timed out", "i/o timeout"} {
			if strings.Contains(text, signal) {
				return true
			}
		}
	}
	return false
}

var dependencyNC = regexp.MustCompile(`(?:^|;[[:space:]]*|[[:space:]]do[[:space:]]+)nc[[:space:]]+(?:-(?:z|w[[:space:]]*[0-9]+)[[:space:]]+)*"?\$(?:DB_HOST|\{DB_HOST\})"?[[:space:]]+"?\$(?:DB_PORT|\{DB_PORT\})"?(?:$|[[:space:];|&])`)

func dependencyNCCommand(c kube.Container) bool {
	tokens := append(append([]string{}, c.Command...), c.Args...)
	return len(tokens) == 3 && (tokens[0] == "sh" || tokens[0] == "/bin/sh") && tokens[1] == "-c" && dependencyNC.MatchString(tokens[2])
}

func dependencyServiceHost(host, namespace string) (string, bool) {
	parts := strings.Split(host, ".")
	if len(validation.IsDNS1123Label(parts[0])) > 0 {
		return "", false
	}
	switch len(parts) {
	case 1:
		return parts[0], true
	case 3:
		if parts[1] == namespace && parts[2] == "svc" {
			return parts[0], true
		}
	case 5:
		if parts[1] == namespace && parts[2] == "svc" && parts[3] == "cluster" && parts[4] == "local" {
			return parts[0], true
		}
	}
	return "", false
}

type dependencyPort struct {
	Name       string `json:"name"`
	Protocol   string `json:"protocol"`
	Port       int    `json:"port"`
	TargetPort any    `json:"targetPort"`
}

func dependencyReadyEndpoint(raw []map[string]any, namespace, name string, servicePort dependencyPort) bool {
	for _, item := range raw {
		var slice struct {
			Metadata  kube.ObjectMeta  `json:"metadata"`
			Ports     []dependencyPort `json:"ports"`
			Endpoints []struct {
				Addresses  []string `json:"addresses"`
				Conditions struct {
					Ready       *bool `json:"ready"`
					Terminating bool  `json:"terminating"`
				} `json:"conditions"`
			} `json:"endpoints"`
		}
		data, err := json.Marshal(item)
		if err != nil || json.Unmarshal(data, &slice) != nil || slice.Metadata.Namespace != namespace || slice.Metadata.Labels["kubernetes.io/service-name"] != name {
			continue
		}
		portMatches := false
		for _, p := range slice.Ports {
			if p.Protocol != "" && p.Protocol != "TCP" {
				continue
			}
			if p.Name != servicePort.Name {
				continue
			}
			switch target := servicePort.TargetPort.(type) {
			case nil:
				portMatches = p.Port == servicePort.Port
			case float64:
				portMatches = float64(p.Port) == target
			case string:
				portMatches = target != "" && p.Name == servicePort.Name && p.Port > 0
			}
			if portMatches {
				break
			}
		}
		if !portMatches {
			continue
		}
		for _, endpoint := range slice.Endpoints {
			if len(endpoint.Addresses) > 0 && endpoint.Conditions.Ready != nil && *endpoint.Conditions.Ready && !endpoint.Conditions.Terminating {
				return true
			}
		}
	}
	return false
}
