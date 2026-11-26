// Package pdbfix keeps disruption-policy evidence and repair authority independent of workload repairs.
package pdbfix

import (
	"context"
	"github.com/fixora/kubectl-fixora/internal/kube"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
)

type Target struct{ Kind, Name string }
type Reader interface {
	GetPDB(context.Context, string, string) (*policyv1.PodDisruptionBudget, error)
	GetWorkload(context.Context, string, string, string) (map[string]any, error)
	ListPods(context.Context, string) ([]corev1.Pod, error)
	ListPDBs(context.Context, string) ([]policyv1.PodDisruptionBudget, error)
	ListHPAs(context.Context, string) ([]autoscalingv2.HorizontalPodAutoscaler, error)
	ResolveOwner(context.Context, corev1.Pod) (kube.WorkloadOwner, error)
}
type EvidenceIssue struct{ Code, Resource, Detail string }
type Diagnosis struct{ Code, Severity, Cause, Next string }
type Snapshot struct {
	PDB      *policyv1.PodDisruptionBudget
	Workload map[string]any
	Pods     []corev1.Pod
	Budgets  []policyv1.PodDisruptionBudget
	HPAs     []autoscalingv2.HorizontalPodAutoscaler
	Owners   map[string]kube.WorkloadOwner
	Issues   []EvidenceIssue
}
