// Package hpafix implements narrowly scoped, operator-authorized HPA repairs.
package hpafix

import (
	"context"
	"os"

	"github.com/fixora/kubectl-fixora/internal/kube"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
)

type Reader interface {
	GetHPA(context.Context, string, string) (*autoscalingv2.HorizontalPodAutoscaler, error)
	GetWorkload(context.Context, string, string, string) (map[string]any, error)
	ListPods(context.Context, string) ([]corev1.Pod, error)
	ListHPAs(context.Context, string) ([]autoscalingv2.HorizontalPodAutoscaler, error)
	ListPodMetrics(context.Context, string) ([]map[string]any, error)
	ResolveOwner(context.Context, corev1.Pod) (kube.WorkloadOwner, error)
}

type Plan struct {
	Namespace, HPAName, TargetKind, TargetName string
	ChangedPaths                               []string
	original, candidate                        map[string]any
	policy                                     *Authority
	policyDigest, planDigest                   string
}

type requestIntent struct {
	Container string `json:"container"`
	CPU       string `json:"cpu,omitempty"`
	Memory    string `json:"memory,omitempty"`
}

type policyDocument struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Metadata   struct {
		Name      string `json:"name"`
		Namespace string `json:"namespace"`
	} `json:"metadata"`
	Spec struct {
		HPA struct {
			Name string `json:"name"`
		} `json:"hpa"`
		Target struct {
			Kind string `json:"kind"`
			Name string `json:"name"`
		} `json:"target"`
		Requests []requestIntent `json:"requests"`
	} `json:"spec"`
}

type Authority struct {
	path     string
	digest   [32]byte
	identity os.FileInfo
	intent   policyDocument
}

type PlanSummary struct {
	Namespace    string   `json:"namespace"`
	HPA          string   `json:"hpa"`
	Target       string   `json:"target"`
	ChangedPaths []string `json:"changedPaths"`
}

type RequestChange struct { Container string `json:"container"`; Resource string `json:"resource"`; Value string `json:"value"` }

func (p Plan) Summary() PlanSummary {
	return PlanSummary{Namespace: p.Namespace, HPA: p.HPAName, Target: p.TargetKind + "/" + p.TargetName, ChangedPaths: append([]string(nil), p.ChangedPaths...)}
}

func (p Plan) RequestChanges() []RequestChange {
	out:=make([]RequestChange,0,len(p.ChangedPaths));containers,_:=nestedMap(p.candidate,"spec","template","spec")["containers"].([]any)
	for _,path:=range p.ChangedPaths{name,resource,ok:=parseChangedPath(path);if !ok{continue};for _,raw:=range containers{c,ok:=raw.(map[string]any);if !ok||strMap(c["name"])!=name{continue};value:=nestedMap(c,"resources","requests")[resource];out=append(out,RequestChange{Container:name,Resource:resource,Value:strMap(value)});break}}
	return out
}
