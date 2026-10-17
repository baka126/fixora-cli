package analyzer

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/fixora/kubectl-fixora/internal/kube"
	"github.com/fixora/kubectl-fixora/internal/pdbfix"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	"strings"
)

// Read namespace evidence from the underlying reader, never display-filtered caches.
type pdbEvidenceReader struct{ kube.Reader }

func (r pdbEvidenceReader) GetPDB(ctx context.Context, ns, name string) (*policyv1.PodDisruptionBudget, error) {
	m, e := r.Reader.GetResource(ctx, ns, "pdb/"+name)
	if e != nil {
		return nil, e
	}
	var p policyv1.PodDisruptionBudget
	if e = decodePDBObject(m, &p); e != nil {
		return nil, e
	}
	return &p, nil
}
func (r pdbEvidenceReader) GetWorkload(ctx context.Context, ns, kind, name string) (map[string]any, error) {
	return r.Reader.GetResource(ctx, ns, strings.ToLower(kind)+"/"+name)
}
func (r pdbEvidenceReader) ListPods(ctx context.Context, ns string) ([]corev1.Pod, error) {
	return pdbItems[corev1.Pod](ctx, r.Reader, ns, "pods")
}
func (r pdbEvidenceReader) ListPDBs(ctx context.Context, ns string) ([]policyv1.PodDisruptionBudget, error) {
	return pdbItems[policyv1.PodDisruptionBudget](ctx, r.Reader, ns, "pdb")
}
func (r pdbEvidenceReader) ListHPAs(ctx context.Context, ns string) ([]autoscalingv2.HorizontalPodAutoscaler, error) {
	return pdbItems[autoscalingv2.HorizontalPodAutoscaler](ctx, r.Reader, ns, "hpa")
}
func (r pdbEvidenceReader) ResolveOwner(ctx context.Context, p corev1.Pod) (kube.WorkloadOwner, error) {
	var kp kube.Pod
	if e := decodePDBObject(p, &kp); e != nil {
		return kube.WorkloadOwner{}, e
	}
	return kube.ResolveWorkloadOwner(ctx, r.Reader, kp)
}
func pdbItems[T any](ctx context.Context, r kube.Reader, ns, kind string) ([]T, error) {
	items, e := r.GetResourceItems(ctx, ns, false, kind)
	if e != nil {
		return nil, e
	}
	out := make([]T, 0, len(items))
	for _, m := range items {
		var v T
		if e = decodePDBObject(m, &v); e != nil {
			return nil, fmt.Errorf("invalid %s evidence: %w", kind, e)
		}
		out = append(out, v)
	}
	return out, nil
}
func decodePDBObject(in, out any) error {
	b, e := json.Marshal(in)
	if e != nil {
		return e
	}
	return json.Unmarshal(b, out)
}

var _ pdbfix.Reader = pdbEvidenceReader{}
