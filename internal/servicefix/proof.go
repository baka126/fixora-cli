package servicefix

import (
	"context"
	"errors"
	corev1 "k8s.io/api/core/v1"
)

type Proof struct {
	reportBinding string
	authority     *Authority
	before        Snapshot
	candidate     *corev1.Service
	sourceOnly    bool
	portIndex     int
	binding       string
}

func (p Proof) contentBinding() string {
	if p.authority == nil {
		return ""
	}
	return digest([]any{p.authority.digest, p.authority.intent, snapshotBinding(p.before), p.candidate, p.sourceOnly, p.portIndex, p.reportBinding})
}
func (p Proof) CheckFresh(ctx context.Context, r Reader) error {
	ctx, cancel := operationContext(ctx)
	defer cancel()
	if p.binding == "" || p.binding != p.contentBinding() {
		return errors.New("fresh immutable Service proof required")
	}
	s, err := Collect(ctx, r, p.authority)
	if err != nil {
		return err
	}
	if snapshotBinding(s) != snapshotBinding(p.before) {
		return errors.New("Service, namespace or backend evidence changed; prepare again")
	}
	return nil
}
func snapshotBinding(s Snapshot) string {
	if s.Service == nil || s.Namespace == nil {
		return ""
	}
	pods := map[string]any{}
	for _, p := range s.Pods {
		pods[string(p.UID)] = []any{p.Name, p.Namespace, p.Labels, p.Spec, p.Status.PodIP, p.Status.PodIPs, podReady(p), p.DeletionTimestamp, s.Owners[string(p.UID)].Kind, s.Owners[string(p.UID)].Name, s.Owners[string(p.UID)].UID}
	}
	meta, _ := s.Backend["metadata"].(map[string]any)
	return digest([]any{s.Service.ResourceVersion, s.Service.Spec, serviceMetadata(s.Service), s.Namespace.UID, s.Namespace.Labels, s.Namespace.Annotations, s.Namespace.DeletionTimestamp, meta["uid"], meta["labels"], meta["annotations"], s.Backend["spec"], pods, s.Slices})
}

func (p *Proof) checkPlan(plan Plan) error {
	if p == nil || p.reportBinding == "" || digest(plan) != p.reportBinding {
		return errors.New("Service preview changed; prepare again")
	}
	return nil
}
