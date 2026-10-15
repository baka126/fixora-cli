package servicefix

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"reflect"
)

// Server-owned TypeMeta, resourceVersion, generation, creation timestamp and
// managedFields may change on admission/update. Other metadata is part of intent.
func serviceMetadata(s *corev1.Service) metav1.ObjectMeta {
	return metav1.ObjectMeta{Name: s.Name, Namespace: s.Namespace, GenerateName: s.GenerateName, UID: s.UID, Labels: s.Labels, Annotations: s.Annotations, OwnerReferences: s.OwnerReferences, Finalizers: s.Finalizers, DeletionTimestamp: s.DeletionTimestamp, DeletionGracePeriodSeconds: s.DeletionGracePeriodSeconds}
}
func sameServiceIntent(want, got *corev1.Service) bool {
	return want != nil && got != nil && reflect.DeepEqual(want.Spec, got.Spec) && reflect.DeepEqual(serviceMetadata(want), serviceMetadata(got))
}
