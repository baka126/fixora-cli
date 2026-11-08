package servicefix

import (
	"context"
	"fmt"
	"reflect"
	"strings"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// A create response can be lost after persistence. Recover only a unique object
// with the unpredictable local attempt label and the expected generated template.
// Capture its actual UID before deletion. An ambiguous/unreadable inventory is
// reported, never converted into permission to delete by name or label alone.
type pendingCreate struct {
	kind    string
	service *corev1.Service
	policy  *networkingv1.NetworkPolicy
	pod     *corev1.Pod
}

func (p pendingCreate) meta() metav1.ObjectMeta {
	switch p.kind {
	case "Service":
		return p.service.ObjectMeta
	case "NetworkPolicy":
		return p.policy.ObjectMeta
	default:
		return p.pod.ObjectMeta
	}
}
func recoverCreate(ctx context.Context, r Runtime, p pendingCreate, nonce string) (*ObjectRef, CleanupResult) {
	meta := p.meta()
	report := CleanupResult{ObjectRef: ObjectRef{Kind: p.kind, Namespace: meta.Namespace, Name: meta.GenerateName + "*"}, Status: "cleanup-incomplete", Detail: fmt.Sprintf("Create outcome unresolved; inspect label %s=%s in this namespace", verifierLabel, nonce)}
	refs, err := r.FindAttempt(ctx, p.kind, meta.Namespace, nonce)
	if err != nil || len(refs) != 1 {
		return nil, report
	}
	ref := refs[0]
	if ref.Kind != p.kind || ref.Namespace != meta.Namespace || ref.Name == "" || ref.UID == "" {
		return nil, report
	}
	report.ObjectRef = ref
	var actual metav1.ObjectMeta
	valid := false
	switch p.kind {
	case "Service":
		got, e := r.GetService(ctx, ref.Namespace, ref.Name)
		if e == nil && got != nil {
			actual = got.ObjectMeta
			valid = checkTemporary(p.service, got) == nil
		}
	case "NetworkPolicy":
		got, e := r.ReadPolicy(ctx, ref.Namespace, ref.Name)
		if e == nil && got != nil {
			actual = got.ObjectMeta
			valid = reflect.DeepEqual(got.Spec, p.policy.Spec)
		}
	case "Pod":
		got, e := r.ReadPod(ctx, ref.Namespace, ref.Name)
		if e == nil && got != nil {
			actual = got.ObjectMeta
			valid = checkProbe(p.pod, got) == nil
		}
	}
	if !valid || actual.Name != ref.Name || string(actual.UID) != ref.UID || actual.Namespace != meta.Namespace || actual.GenerateName != meta.GenerateName || !strings.HasPrefix(actual.Name, meta.GenerateName) || actual.DeletionTimestamp != nil || !reflect.DeepEqual(actual.Labels, meta.Labels) || !reflect.DeepEqual(actual.Annotations, meta.Annotations) || !reflect.DeepEqual(actual.OwnerReferences, meta.OwnerReferences) || !reflect.DeepEqual(actual.Finalizers, meta.Finalizers) {
		return nil, report
	}
	return &ref, CleanupResult{}
}
